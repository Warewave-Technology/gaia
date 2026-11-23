#!/usr/bin/env bash
# fixture: the lab IdP for T26 (ADR-008 §1-§4, §9). Not part of the product. Keycloak in a podman container on gaia-lab,
# HTTPS on gaia-lab.test:8444 with a self-signed certificate, and a realm "gaia" made by import:
#   - a confidential client "gaia": code flow with PKCE (S256), the device authorization grant, a groups claim (group
#     names, not paths) in the ID token, and acr "mfa" for level of authentication 2 (default and requested);
#   - a browser flow with one level of authentication, the password and an OTP code, asked at every sign-in (max age 0)
#     and mapped to acr "mfa": a code flow, a step-up (prompt=login, max_age=0) and the device grant (which takes no
#     acr_values at Keycloak) all ask for the code;
#   - groups gaia-admins, gaia-operators, internal-audit, staff; users with a password and a TOTP credential:
#       kc-admin (gaia-admins), kc-op (gaia-operators), kc-aud (internal-audit), kc-gone (gaia-operators: the gate
#       removes it from the group), kc-keep (gaia-admins: its session sees the IdP stopped), kc-staff (staff only).
#   The lab's OTP policy lets a code be used more than once within its 30 s (otpPolicyCodeReusable), so the gate's many
#   step-ups need not wait for new codes. This is the lab IdP's policy, not Gaia's: Gaia never takes a code twice.
# Secrets (the client secret, the users' passwords and OTP seeds, Keycloak's admin password) are generated here into
# $DIR/secrets.json (0600, root) and never printed.
#
# Usage (on gaia-lab, as root): lab/lab-vm/t26-keycloak.sh up | stop | start | down | status | redirect URL | user NAME GROUP
#   redirect URL     lets the Gaia server at URL (https://name:port) use the client too (T29: the PO's server)
#   user NAME GROUP  a user of one group (gaia-admins, gaia-operators, internal-audit) with a first password that must
#                    be changed and an OTP to enrol at the first sign-in (T29: the PO's own SSO user); the password is in
#                    secrets.json (.users.NAME.password), for the PO to read; it is never printed
set -euo pipefail

DIR=${KC_DIR:-/root/t26-keycloak}
NAME=gaia-t26-keycloak
IMAGE=quay.io/keycloak/keycloak:26.4
HOSTNAME_=gaia-lab.test
PORT=8444
GAIA_URLS=${GAIA_URLS:-https://gaia-lab.test:8446}
((EUID == 0)) || { echo "run as root"; exit 3; }
log() { printf '[%s] %s\n' "$(date -u +%H:%M:%S)" "$*" >&2; }

wait_ready() {
	for ((i = 0; i < 120; i++)); do
		if curl -fs --cacert "$DIR/certs/kc.crt" "https://$HOSTNAME_:$PORT/realms/gaia/.well-known/openid-configuration" >/dev/null 2>&1; then
			return 0
		fi
		sleep 2
	done
	echo "UNKNOWN: Keycloak did not answer within 4 minutes"
	podman logs --tail 30 "$NAME" >&2 || true
	exit 3
}

# The container runs under a unit of its own (podman start -a), so it outlives the command that made it (a gate's unit
# ends with its processes).
run_unit() {
	systemctl stop "$NAME" 2>/dev/null || true
	systemctl reset-failed "$NAME" 2>/dev/null || true
	systemd-run --unit="$NAME" --collect -p KillMode=mixed podman start -a "$NAME" >/dev/null
}

status() {
	local st digest
	st=$(podman inspect -f '{{.State.Status}}' "$NAME" 2>/dev/null || echo absent)
	digest=$(podman image inspect -f '{{.Digest}}' "$IMAGE" 2>/dev/null || echo none)
	jq -n --arg st "$st" --arg img "$IMAGE" --arg d "$digest" --arg url "https://$HOSTNAME_:$PORT/realms/gaia" \
		'{container: $st, image: $img, digest: $d, issuer: $url}'
}

case "${1:-}" in
stop)
	podman stop -t 10 "$NAME" >/dev/null
	systemctl stop "$NAME" 2>/dev/null || true
	status
	exit 0
	;;
start)
	run_unit
	wait_ready
	status
	exit 0
	;;
down)
	podman rm -f "$NAME" >/dev/null 2>&1 || true
	systemctl stop "$NAME" 2>/dev/null || true
	rm -rf "$DIR"
	sed -i "/ $HOSTNAME_\$/d" /etc/hosts
	exit 0
	;;
status)
	status
	exit 0
	;;
redirect | user)
	# Keycloak's admin REST API from here: the admin password is read from secrets.json by Python, never on a command line.
	python3 -I - "$DIR/secrets.json" "$DIR/certs/kc.crt" "https://$HOSTNAME_:$PORT" "$@" <<'EOF'
import json, secrets, ssl, sys, urllib.parse, urllib.request
path, ca, base, mode, args = sys.argv[1], sys.argv[2], sys.argv[3], sys.argv[4], sys.argv[5:]
sec = json.load(open(path))
ctx = ssl.create_default_context(cafile=ca)
def call(method, url, body=None, token=None, form=False):
    data, headers = None, {}
    if body is not None:
        data = urllib.parse.urlencode(body).encode() if form else json.dumps(body).encode()
        headers["Content-Type"] = "application/x-www-form-urlencoded" if form else "application/json"
    if token:
        headers["Authorization"] = "Bearer " + token
    with urllib.request.urlopen(urllib.request.Request(base + url, data=data, headers=headers, method=method), context=ctx) as r:
        b = r.read()
        return json.loads(b) if b else None
tok = call("POST", "/realms/master/protocol/openid-connect/token", {"grant_type": "password", "client_id": "admin-cli",
           "username": "admin", "password": sec["admin_password"]}, form=True)["access_token"]
if mode == "redirect":
    if len(args) != 1 or not args[0].startswith("https://"):
        sys.exit("usage: redirect https://NAME:PORT")
    c = call("GET", "/admin/realms/gaia/clients?clientId=gaia", token=tok)[0]
    uri = args[0].rstrip("/") + "/api/v1/oidc/callback"
    if uri not in c["redirectUris"]:
        c["redirectUris"].append(uri)
        call("PUT", "/admin/realms/gaia/clients/" + c["id"], c, token=tok)
    print(json.dumps({"client": "gaia", "redirect_uris": c["redirectUris"]}))
else:
    if len(args) != 2 or args[1] not in ("gaia-admins", "gaia-operators", "internal-audit"):
        sys.exit("usage: user NAME gaia-admins|gaia-operators|internal-audit")
    name, group = args
    if call("GET", "/admin/realms/gaia/users?exact=true&username=" + urllib.parse.quote(name), token=tok):
        sys.exit(f"user {name} exists")
    pw = "lab " + secrets.token_urlsafe(18)
    call("POST", "/admin/realms/gaia/users", {"username": name, "enabled": True, "email": f"{name}@lab.test", "emailVerified": True,
         "firstName": name, "lastName": "Lab", "groups": ["/" + group], "requiredActions": ["CONFIGURE_TOTP", "UPDATE_PASSWORD"],
         "credentials": [{"type": "password", "value": pw, "temporary": True}]}, token=tok)
    sec["users"][name] = {"password": pw, "otp": None, "group": group}
    json.dump(sec, open(path, "w"), indent=2)
    print(json.dumps({"user": name, "group": group, "first_password": f"{path} .users.{name}.password (change it at the first sign-in)",
                      "otp": "enrolled at the first sign-in"}))
EOF
	exit 0
	;;
up) ;;
*)
	echo "usage: $0 up | stop | start | down | status | redirect URL | user NAME GROUP"
	exit 3
	;;
esac

# The lab name resolves to gaia-lab's LAN address here too (the gate server and Keycloak call each other by it).
lan=$(hostname -I | awk '{ print $1 }')
sed -i "/ $HOSTNAME_\$/d" /etc/hosts
echo "$lan $HOSTNAME_" >>/etc/hosts

# The container's user reads the two mounted directories; $DIR itself (and /root) keep everyone else out.
install -d -m 0700 "$DIR"
install -d -m 0755 "$DIR/certs" "$DIR/import"
if [[ ! -f "$DIR/certs/kc.crt" ]]; then
	openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes -days 30 -subj "/CN=$HOSTNAME_/O=Gaia lab IdP" \
		-addext "subjectAltName=DNS:$HOSTNAME_" -keyout "$DIR/certs/kc.key" -out "$DIR/certs/kc.crt" 2>/dev/null
	chmod 0644 "$DIR/certs/kc.crt" "$DIR/certs/kc.key" # read by the container's user; the directory is root's 0700
fi
if [[ ! -f "$DIR/secrets.json" ]]; then
	umask 077
	python3 -I - "$DIR/secrets.json" <<'EOF'
import json, secrets, string, sys
alpha = string.ascii_letters + string.digits
pw = lambda: "lab " + secrets.token_urlsafe(18)
users = {u: {"password": pw(), "otp": "".join(secrets.choice(alpha) for _ in range(20))}
         for u in ["kc-admin", "kc-op", "kc-aud", "kc-gone", "kc-keep", "kc-staff"]}
json.dump({"client_secret": secrets.token_urlsafe(32), "admin_password": secrets.token_urlsafe(24), "users": users},
          open(sys.argv[1], "w"), indent=2)
EOF
fi
python3 -I - "$DIR/secrets.json" "$DIR/import/gaia-realm.json" "$GAIA_URLS" <<'EOF'
import json, sys
sec = json.load(open(sys.argv[1]))
groups = {"kc-admin": ["/gaia-admins"], "kc-op": ["/gaia-operators"], "kc-aud": ["/internal-audit"], "kc-gone": ["/gaia-operators"],
          "kc-keep": ["/gaia-admins"], "kc-staff": ["/staff"]}
def user(name):
    u = sec["users"][name]
    return {"username": name, "enabled": True, "email": f"{name}@lab.test", "emailVerified": True, "firstName": name, "lastName": "Lab",
            "groups": groups[name], "credentials": [
                {"type": "password", "value": u["password"], "temporary": False},
                {"type": "otp", "userLabel": "lab", "secretData": json.dumps({"value": u["otp"]}),
                 "credentialData": json.dumps({"subType": "totp", "digits": 6, "period": 30, "algorithm": "HmacSHA1", "counter": 0})}]}
ex = lambda a, prio, req="REQUIRED", **kw: {"authenticator": a, "requirement": req, "priority": prio, "autheticatorFlow": False, "userSetupAllowed": False, **kw}
sub = lambda alias, prio, req: {"flowAlias": alias, "requirement": req, "priority": prio, "autheticatorFlow": True, "userSetupAllowed": False}
flow = lambda alias, execs, top=False: {"alias": alias, "description": "Gaia lab: " + alias, "providerId": "basic-flow", "topLevel": top,
                                        "builtIn": False, "authenticationExecutions": execs}
urls = sys.argv[3].split(",")
realm = {
    "realm": "gaia", "enabled": True, "sslRequired": "external", "browserFlow": "gaia-browser",
    "otpPolicyType": "totp", "otpPolicyAlgorithm": "HmacSHA1", "otpPolicyDigits": 6, "otpPolicyPeriod": 30, "otpPolicyLookAheadWindow": 1,
    "otpPolicyCodeReusable": True, "ssoSessionIdleTimeout": 3600, "ssoSessionMaxLifespan": 43200,
    "groups": [{"name": g} for g in ["gaia-admins", "gaia-operators", "internal-audit", "staff"]],
    "clients": [{
        "clientId": "gaia", "name": "Gaia (T26 gate)", "enabled": True, "publicClient": False, "clientAuthenticatorType": "client-secret",
        "secret": sec["client_secret"], "standardFlowEnabled": True, "directAccessGrantsEnabled": False, "implicitFlowEnabled": False,
        "serviceAccountsEnabled": False, "consentRequired": False, "redirectUris": [u + "/api/v1/oidc/callback" for u in urls], "webOrigins": [],
        "attributes": {"pkce.code.challenge.method": "S256", "oauth2.device.authorization.grant.enabled": "true",
                       "acr.loa.map": json.dumps({"mfa": 1}), "default.acr.values": "mfa", "use.refresh.tokens": "true"},
        "protocolMappers": [{"name": "groups", "protocol": "openid-connect", "protocolMapper": "oidc-group-membership-mapper",
                             "config": {"claim.name": "groups", "full.path": "false", "id.token.claim": "true", "access.token.claim": "true",
                                        "userinfo.token.claim": "true"}}]}],
    "authenticationFlows": [
        flow("gaia-browser", [ex("auth-cookie", 10, "ALTERNATIVE"), sub("gaia-forms", 20, "ALTERNATIVE")], top=True),
        flow("gaia-forms", [sub("gaia-mfa", 10, "CONDITIONAL")]),
        flow("gaia-mfa", [ex("conditional-level-of-authentication", 10, authenticatorConfig="gaia-mfa"), ex("auth-username-password-form", 20),
                          ex("auth-otp-form", 30)]),
    ],
    "authenticatorConfig": [{"alias": "gaia-mfa", "config": {"loa-condition-level": "1", "loa-max-age": "0"}}],
    "users": [user(n) for n in groups],
}
json.dump(realm, open(sys.argv[2], "w"), indent=2)
EOF
chmod 0644 "$DIR/import/gaia-realm.json" # holds the lab users' secrets: the directory is root's 0700

log "image $IMAGE"
podman image exists "$IMAGE" || podman pull -q "$IMAGE" >/dev/null
systemctl stop "$NAME" 2>/dev/null || true
podman rm -f "$NAME" >/dev/null 2>&1 || true
# The admin password goes in an env file (0600), not on a command line that ps shows.
(umask 077 && jq -r '"KC_BOOTSTRAP_ADMIN_USERNAME=admin\nKC_BOOTSTRAP_ADMIN_PASSWORD=" + .admin_password' "$DIR/secrets.json" >"$DIR/kc.env")
podman create --name "$NAME" --network host --memory 1200m --env-file "$DIR/kc.env" \
	-v "$DIR/certs:/opt/keycloak/conf/certs:ro,Z" -v "$DIR/import:/opt/keycloak/data/import:ro,Z" -e JAVA_OPTS_KC_HEAP="-Xms256m -Xmx768m" \
	"$IMAGE" start-dev --import-realm --http-enabled=false --https-port="$PORT" --hostname="https://$HOSTNAME_:$PORT" \
	--https-certificate-file=/opt/keycloak/conf/certs/kc.crt --https-certificate-key-file=/opt/keycloak/conf/certs/kc.key >/dev/null
run_unit
log "waiting for Keycloak"
wait_ready
status
