#!/usr/bin/env python3
"""Independent verifier for the Restore Evidence Format, version 1 (docs/spec/evidence-v1.md).

Standard library only. Implements bundle mode (§8.4) and ledger mode (§8.2),
including head statements (§6.8, step L5 / §8.6, addition of 2026-11-17).
Version 0 (appendix A) is treated as format_unsupported.

Usage:
  verify.py bundle <bundle_dir> [--key FILE] [--json]
  verify.py ledger <ledger_file> <bundles_dir> [--anchor FILE] [--head FILE]... [--json]

--head may be given several times; statements are checked in the order given.
"""
import base64
import datetime
import hashlib
import json
import os
import re
import stat
import sys

# ---------------------------------------------------------------- Ed25519 (RFC 8032, verify only)
_p = 2**255 - 19
_L = 2**252 + 27742317777372353535851937790883648493
_d = (-121665 * pow(121666, _p - 2, _p)) % _p
_SQRT_M1 = pow(2, (_p - 1) // 4, _p)


def _inv(x):
    return pow(x, _p - 2, _p)


def _padd(P, Q):
    X1, Y1, Z1, T1 = P
    X2, Y2, Z2, T2 = Q
    A = (Y1 - X1) * (Y2 - X2) % _p
    B = (Y1 + X1) * (Y2 + X2) % _p
    C = T1 * 2 * _d * T2 % _p
    D = Z1 * 2 * Z2 % _p
    E, F, G, H = B - A, D - C, D + C, B + A
    return (E * F % _p, G * H % _p, F * G % _p, E * H % _p)


def _pmul(s, P):
    Q = (0, 1, 1, 0)
    while s > 0:
        if s & 1:
            Q = _padd(Q, P)
        P = _padd(P, P)
        s >>= 1
    return Q


def _pneg(P):
    X, Y, Z, T = P
    return ((-X) % _p, Y, Z, (-T) % _p)


def _recover_x(y, sign):
    if y >= _p:
        return None
    x2 = (y * y - 1) * _inv(_d * y * y + 1) % _p
    if x2 == 0:
        return None if sign else 0
    x = pow(x2, (_p + 3) // 8, _p)
    if (x * x - x2) % _p != 0:
        x = x * _SQRT_M1 % _p
    if (x * x - x2) % _p != 0:
        return None
    if (x & 1) != sign:
        x = _p - x
    return x


def _decode_point(b):
    if len(b) != 32:
        return None
    y = int.from_bytes(b, "little")
    sign = y >> 255
    y &= (1 << 255) - 1
    x = _recover_x(y, sign)
    if x is None:
        return None
    return (x, y, 1, x * y % _p)


def _encode_point(P):
    X, Y, Z, _ = P
    zi = _inv(Z)
    x, y = X * zi % _p, Y * zi % _p
    return (y | ((x & 1) << 255)).to_bytes(32, "little")


_gy = 4 * _inv(5) % _p
_B = (_recover_x(_gy, 0), _gy, 1, _recover_x(_gy, 0) * _gy % _p)


def ed25519_verify(pub, msg, sig):
    """Cofactorless verification, R compared by encoding (as Go's crypto/ed25519 does)."""
    if len(pub) != 32 or len(sig) != 64:
        return False
    A = _decode_point(pub)
    if A is None:
        return False
    Rb = sig[:32]
    S = int.from_bytes(sig[32:], "little")
    if S >= _L:
        return False
    h = int.from_bytes(hashlib.sha512(Rb + pub + msg).digest(), "little") % _L
    R2 = _padd(_pmul(S, _B), _pneg(_pmul(h, A)))
    return _encode_point(R2) == Rb


# ---------------------------------------------------------------- building blocks (§2)
HEX64 = re.compile(r"\A[0-9a-f]{64}\Z")
INT_RE = re.compile(r"\A(0|[1-9][0-9]{0,15})\Z")
TIME_RE = re.compile(r"\A([0-9]{4})-([0-9]{2})-([0-9]{2})T([0-9]{2}):([0-9]{2}):([0-9]{2})(\.[0-9]{1,9})?Z\Z")
BUNDLE_ID_RE = re.compile(r"\A[A-Za-z0-9._-]{1,255}\Z")
B64_RE = re.compile(r"\A[A-Za-z0-9+/]*={0,2}\Z")
ZERO_HASH = "0" * 64


def sha256hex(b):
    return hashlib.sha256(b).hexdigest()


class Int:
    """A JSON number token written as an integer (no fraction/exponent)."""
    __slots__ = ("raw",)

    def __init__(self, raw):
        self.raw = raw


class Num:
    """Any other JSON number token."""
    __slots__ = ("raw",)

    def __init__(self, raw):
        self.raw = raw


class JSONError(Exception):
    pass


class VerifyError(Exception):
    """Verification ends with an error and no verdict (§8.2 L1, §8.6)."""


def _pairs(pairs):
    seen = set()
    for k, _ in pairs:
        if k in seen:
            raise JSONError("duplicate member %r" % k)
        seen.add(k)
    return dict(pairs)


def _const(name):
    raise JSONError("non-JSON constant %s" % name)


def parse_json(data):
    """Parse bytes as one JSON text per §2. Returns the value; numbers are Int/Num tokens."""
    if isinstance(data, bytes):
        try:
            text = data.decode("utf-8", errors="strict")
        except UnicodeDecodeError as e:
            raise JSONError(str(e))
    else:
        text = data
    try:
        return json.loads(text, object_pairs_hook=_pairs, parse_int=Int, parse_float=Num,
                          parse_constant=_const)
    except JSONError:
        raise
    except (ValueError, RecursionError) as e:
        raise JSONError(str(e))


def has_lone_surrogate(v):
    """True if any decoded string (member name or value, at any depth) holds a UTF-16
    surrogate code point. A valid pair is combined by the decoder; raw surrogates cannot
    pass strict UTF-8 decoding; so what remains came from an unpaired \\u escape (§6.2)."""
    if isinstance(v, str):
        return any(0xD800 <= ord(c) <= 0xDFFF for c in v)
    if isinstance(v, dict):
        return any(has_lone_surrogate(k) or has_lone_surrogate(x) for k, x in v.items())
    if isinstance(v, list):
        return any(has_lone_surrogate(x) for x in v)
    return False


def is_integer(v):
    return isinstance(v, Int) and INT_RE.match(v.raw) is not None


def int_value(v):
    return int(v.raw)


def is_hex64(v):
    return isinstance(v, str) and HEX64.match(v) is not None


def is_time(v):
    if not isinstance(v, str):
        return False
    m = TIME_RE.match(v)
    if not m:
        return False
    y, mo, d, h, mi, s = (int(x) for x in m.groups()[:6])
    try:
        datetime.datetime(y, mo, d, h, mi, s)
    except ValueError:
        return False
    return True


def is_bundle_id(v):
    return isinstance(v, str) and BUNDLE_ID_RE.match(v) is not None and v not in (".", "..")


def b64decode_strict(s):
    """RFC 4648 §4, padding required, no whitespace, zero trailing bits. Returns bytes or None."""
    if isinstance(s, bytes):
        try:
            s = s.decode("ascii")
        except UnicodeDecodeError:
            return None
    if not isinstance(s, str) or len(s) % 4 != 0 or not B64_RE.match(s):
        return None
    try:
        out = base64.b64decode(s, validate=True)
    except Exception:
        return None
    if base64.b64encode(out).decode("ascii") != s:
        return None
    return out


def key_id(pub):
    return sha256hex(pub)


# ---------------------------------------------------------------- findings
SEVERITY = {
    "format_unsupported": "UNKNOWN",
    "anchor_missing": "UNKNOWN",
    "head_unverifiable": "UNKNOWN",
}


class Findings:
    def __init__(self):
        self.items = []

    def add(self, code, where=""):
        self.items.append((code, where))

    def codes(self):
        return sorted({c for c, _ in self.items})

    def verdict(self):
        if not self.items:
            return "OK"
        if any(SEVERITY.get(c, "CRITICAL") == "CRITICAL" for c, _ in self.items):
            return "CRITICAL"
        return "UNKNOWN"


# ---------------------------------------------------------------- bundles (§5, §8.4)
def path_valid(p):
    if not isinstance(p, str):
        return False
    try:
        b = p.encode("utf-8")
    except UnicodeEncodeError:
        return False
    if not (1 <= len(b) <= 4096) or "\\" in p or "\x00" in p:
        return False
    for seg in p.split("/"):
        if seg in ("", ".", ".."):
            return False
    if p in ("manifest.json", "manifest.sig"):
        return False
    return True


def read_regular(path):
    """Read a regular file without following a symlink. None if it cannot be read."""
    try:
        st = os.lstat(path)
        if not stat.S_ISREG(st.st_mode):
            return None
        with open(path, "rb") as f:
            return f.read()
    except OSError:
        return None


def collect_content(bdir, F, where):
    """§5.1: regular files keyed by UTF-8 path bytes; reports file_type_invalid."""
    files = {}
    root = os.fsencode(bdir)

    def walk(abs_dir, rel):
        try:
            entries = list(os.scandir(abs_dir))
        except OSError:
            return
        for e in entries:
            name = e.name
            relp = rel + b"/" + name if rel else name
            if not rel and name in (b"manifest.json", b"manifest.sig"):
                continue
            try:
                st = os.lstat(e.path)
            except OSError:
                continue
            if stat.S_ISDIR(st.st_mode):
                walk(e.path, relp)
            elif stat.S_ISREG(st.st_mode):
                files[relp] = e.path
            else:
                F.add("file_type_invalid", "%s:%s" % (where, relp.decode("utf-8", "replace")))

    walk(root, b"")
    return files


def check_manifest_members(m):
    """§5.2 members other than format/format_version. True when all present and well-formed."""
    if not is_bundle_id(m.get("bundle_id")):
        return False
    if not is_time(m.get("created")):
        return False
    if not is_hex64(m.get("key_id")):
        return False
    subj = m.get("subject")
    if not isinstance(subj, dict) or not all(isinstance(v, str) for v in subj.values()):
        return False
    files = m.get("files")
    if not isinstance(files, list):
        return False
    for f in files:
        if not isinstance(f, dict):
            return False
        if not isinstance(f.get("path"), str):
            return False
        if not is_hex64(f.get("sha256")):
            return False
        if not is_integer(f.get("bytes")):
            return False
    return True


def bundle_procedure(bdir, K, F, where="bundle"):
    """§8.4. Returns (manifest_bytes or None, parsed manifest dict or None, is_v0)."""
    mbytes = read_regular(os.path.join(bdir, "manifest.json"))
    if mbytes is None:
        F.add("manifest_missing", where)
        return None, None, False
    try:
        m = parse_json(mbytes)
    except JSONError:
        m = None
    if not isinstance(m, dict):
        F.add("manifest_malformed", where)
        return mbytes, None, False
    fmt = m.get("format")
    if fmt == "gaia-evidence/v1":  # appendix A: treated as unsupported
        F.add("format_unsupported", where)
        return mbytes, m, True
    if not isinstance(fmt, str):
        F.add("manifest_malformed", where)
        return mbytes, m, False
    fv = m.get("format_version")
    if fmt != "restore-evidence" or (is_integer(fv) and int_value(fv) != 1):
        F.add("format_unsupported", where)
        return mbytes, m, False
    if not is_integer(fv) or not check_manifest_members(m):
        F.add("manifest_malformed", where)
        return mbytes, m, False

    # 4. signature
    if K is None:
        F.add("anchor_missing", where)
    elif m["key_id"] != key_id(K):
        F.add("key_mismatch", where)
    else:
        sigb = read_regular(os.path.join(bdir, "manifest.sig"))
        if sigb is None:
            F.add("signature_missing", where)
        else:
            if sigb.endswith(b"\n"):
                sigb = sigb[:-1]
            sig = b64decode_strict(sigb)
            if sig is None or len(sig) != 64:
                F.add("signature_malformed", where)
            elif not ed25519_verify(K, mbytes, sig):
                F.add("signature_invalid", where)

    # 5. paths
    listed = {}
    prev = None
    for f in m["files"]:
        p = f["path"]
        if not path_valid(p):
            F.add("path_invalid", "%s:%r" % (where, p))
            continue
        pb = p.encode("utf-8")
        if prev is not None and not pb > prev:
            F.add("path_invalid", "%s:%r" % (where, p))
        prev = pb
        listed.setdefault(pb, f)

    # 6. content
    content = collect_content(bdir, F, where)

    # 7. listed files
    for pb, f in listed.items():
        ap = content.get(pb)
        if ap is None:
            F.add("file_missing", "%s:%s" % (where, pb.decode()))
            continue
        data = read_regular(ap)
        if data is None or len(data) != int_value(f["bytes"]) or sha256hex(data) != f["sha256"]:
            F.add("file_modified", "%s:%s" % (where, pb.decode()))

    # 8. unlisted
    for pb in content:
        if pb not in listed:
            F.add("file_unlisted", "%s:%s" % (where, pb.decode("utf-8", "replace")))
    return mbytes, m, False


# ---------------------------------------------------------------- ledger (§6, §8.2)
def check_entry_members(e):
    seq = e.get("seq")
    if not is_integer(seq) or int_value(seq) < 1:
        return False
    if not is_time(e.get("time")):
        return False
    t = e.get("type")
    if t not in ("key", "bundle"):
        return False
    if not is_hex64(e.get("key_id")) or not is_hex64(e.get("prev")):
        return False
    if t == "key":
        ev = e.get("event")
        if ev not in ("genesis", "rotate"):
            return False
        if not isinstance(e.get("public_key"), str):
            return False
        if ev == "rotate" and not is_hex64(e.get("new_key_id")):
            return False
    else:
        if not is_bundle_id(e.get("bundle_id")):
            return False
        if not is_hex64(e.get("bundle_sha256")):
            return False
        if not isinstance(e.get("subject"), str) or not isinstance(e.get("verdict"), str):
            return False
    return True


def decode_pub(s):
    b = b64decode_strict(s) if isinstance(s, str) else None
    return b if b is not None and len(b) == 32 else None


def ledger_mode(ledger_path, D, A, F, heads=()):
    """§8.2. heads: list of (label, bytes or None) head statements, in the order given."""
    refs = []  # (bundle_id, sha, line, K)
    data = None
    # What L5 (§8.6) uses from L2:
    entry_hash = {}   # n -> H(n)
    key_after = {}    # n -> K(n), the key current after entry n (step 11)
    known = []        # known keys: A if given, accepted genesis key, keys a rotation made current
    A_given = A       # L2 may set A from the genesis entry; L uses only a given A
    stopped_unsupported = False  # L2 stopped with format_unsupported (§8.6 H4)
    if A is not None:
        known.append(A)
    # L1
    if not os.path.lexists(ledger_path):
        F.add("ledger_missing")
    else:
        try:
            with open(ledger_path, "rb") as f:
                data = f.read()
        except OSError as e:
            # L1: exists but cannot be read -> error, no verdict
            raise VerifyError("cannot read ledger %s: %s" % (ledger_path, e))
        if data is not None and len(data) == 0:
            F.add("ledger_empty")
            data = None
    v0 = False
    if data is not None:
        first = data.split(b"\n", 1)[0]
        try:
            fo = parse_json(first)
            v0 = isinstance(fo, dict) and fo.get("format") == "gaia-ledger/v1"
        except JSONError:
            pass
        if v0:
            F.add("format_unsupported", "ledger")
            # L1: "report format_unsupported once for each head statement"
            for label, _ in heads:
                F.add("format_unsupported", label)
            # without appendix A: "ends verification: L2-L5 add nothing"
            return

    if data is not None:
        # L2
        ends_lf = data.endswith(b"\n")
        lines = data.split(b"\n")
        if ends_lf:
            lines = lines[:-1]
        K = None
        P = ZERO_HASH
        seen_ids = set()
        for idx, line in enumerate(lines):
            n = idx + 1
            loc = "line %d" % n
            if n == len(lines) and not ends_lf:
                F.add("entry_malformed", loc)
                break
            try:
                obj = parse_json(line)
            except JSONError:
                obj = None
            if not isinstance(obj, dict) or not isinstance(obj.get("payload"), str) \
                    or not isinstance(obj.get("sig"), str) or has_lone_surrogate(obj):
                F.add("entry_malformed", loc)
                break
            try:
                pbytes = obj["payload"].encode("utf-8")
            except UnicodeEncodeError:
                F.add("entry_malformed", loc)
                break
            try:
                e = parse_json(pbytes)
            except JSONError:
                e = None
            if not isinstance(e, dict):
                F.add("entry_malformed", loc)
                break
            if e.get("format") != "restore-evidence-ledger" or not is_integer(e.get("format_version")):
                F.add("entry_malformed", loc)
                break
            if int_value(e["format_version"]) != 1:
                F.add("format_unsupported", loc)
                stopped_unsupported = True
                break
            if not check_entry_members(e):
                F.add("entry_malformed", loc)
                break
            H = sha256hex(pbytes)
            before = len(F.items)
            # 6
            if int_value(e["seq"]) != n:
                F.add("seq_invalid", loc)
            # 7
            if e["prev"] != P:
                F.add("chain_broken", loc)
            # 8
            stop = False
            if n == 1:
                if not (e["type"] == "key" and e["event"] == "genesis"):
                    F.add("anchor_mismatch", loc)
                    break
                gk = decode_pub(e["public_key"])
                if gk is None or key_id(gk) != e["key_id"]:
                    F.add("key_entry_invalid", loc)
                    break
                if A is None:
                    F.add("anchor_missing", loc)
                    A = gk
                elif A != gk:
                    F.add("anchor_mismatch", loc)
                    break
                K = A
                known.append(gk)
            elif e["key_id"] != key_id(K):
                F.add("signer_invalid", loc)
            if stop:
                break
            # 9
            sig = b64decode_strict(obj["sig"])
            if sig is None or len(sig) != 64 or not ed25519_verify(K, pbytes, sig):
                F.add("entry_signature_invalid", loc)
            # 10
            newK = None
            if e["type"] == "key":
                if e["event"] == "genesis":
                    if n > 1:
                        F.add("key_entry_invalid", loc)
                else:
                    nk = decode_pub(e["public_key"])
                    if nk is None or key_id(nk) != e["new_key_id"] or nk == K:
                        F.add("key_entry_invalid", loc)
                    else:
                        newK = nk
            else:
                bid = e["bundle_id"]
                if bid in seen_ids:
                    F.add("bundle_duplicate", loc)
                seen_ids.add(bid)
                refs.append((bid, e["bundle_sha256"], n, K))
            # 11
            if newK is not None and len(F.items) == before:
                K = newK
                known.append(newK)
            P = H
            # Recorded for L5. Interpretation: an entry counts towards N only once it has
            # reached step 11 (an entry 1 that stops in step 8 has no K(1)).
            entry_hash[n] = H
            key_after[n] = K

        # L3
        for bid, bsha, n, RK in refs:
            bdir = os.path.join(D, bid)
            try:
                isdir = stat.S_ISDIR(os.lstat(bdir).st_mode)
            except OSError:
                isdir = False
            if not isdir:
                F.add("bundle_missing", bid)
                continue
            mbytes, m, is_v0 = bundle_procedure(bdir, RK, F, where=bid)
            if is_v0:
                continue
            if mbytes is not None and sha256hex(mbytes) != bsha:
                F.add("bundle_hash_mismatch", bid)
            if m is not None and "bundle_id" in m and m["bundle_id"] != bid:
                F.add("bundle_id_mismatch", bid)

    # L4
    ref_ids = {r[0] for r in refs}
    if os.path.lexists(D):
        try:
            entries = list(os.scandir(D))
        except OSError:
            F.add("bundle_missing", "bundles directory unreadable")
            entries = []
        for ent in entries:
            try:
                isdir = stat.S_ISDIR(os.lstat(ent.path).st_mode)
            except OSError:
                continue
            if isdir and ent.name not in ref_ids:
                F.add("bundle_unlisted", ent.name)

    # L5 (§8.6)
    N = len(entry_hash)
    L = key_after[N] if N >= 1 else A_given
    head_statements(heads, N, entry_hash, key_after, L, known, F, stopped_unsupported)


def head_statements(heads, N, entry_hash, key_after, L, known, F, stopped_unsupported=False):
    """§8.6, steps H1-H4. Each statement moves on to the next after its first finding."""
    for label, raw in heads:
        # H1
        obj = None
        if raw is not None:
            try:
                obj = parse_json(raw)
            except JSONError:
                obj = None
        if not isinstance(obj, dict) or not isinstance(obj.get("payload"), str) \
                or not isinstance(obj.get("sig"), str) or has_lone_surrogate(obj):
            F.add("head_malformed", label)
            continue
        pbytes = obj["payload"].encode("utf-8")  # cannot fail: surrogates excluded above
        # H2
        try:
            h = parse_json(pbytes)
        except JSONError:
            h = None
        if not isinstance(h, dict) or h.get("format") != "restore-evidence-head" \
                or not is_integer(h.get("format_version")):
            F.add("head_malformed", label)
            continue
        if int_value(h["format_version"]) != 1:
            F.add("format_unsupported", label)
            continue
        # H3
        seq = h.get("seq")
        if not is_time(h.get("time")) or not is_hex64(h.get("key_id")) \
                or not is_hex64(h.get("entry_sha256")) \
                or not is_integer(seq) or int_value(seq) < 1:
            F.add("head_malformed", label)
            continue
        # H4
        n = int_value(seq)
        sig = b64decode_strict(obj["sig"])
        if sig is not None and len(sig) != 64:
            sig = None
        if n <= N:
            Kn = key_after[n]
            if h["key_id"] != key_id(Kn) or sig is None or not ed25519_verify(Kn, pbytes, sig):
                F.add("head_signature_invalid", "%s seq %d" % (label, n))
            elif h["entry_sha256"] != entry_hash[n]:
                F.add("head_mismatch", "%s seq %d" % (label, n))
        else:
            at = "%s seq %d > %d" % (label, n, N)
            if stopped_unsupported:
                F.add("head_unverifiable", at + " (L2 stopped at an unsupported entry version)")
            elif L is not None and h["key_id"] == key_id(L):
                if sig is not None and ed25519_verify(L, pbytes, sig):
                    F.add("ledger_truncated", at)
                else:
                    F.add("head_signature_invalid", at)
            elif any(key_id(k) == h["key_id"] for k in known):
                F.add("head_signature_invalid", at + " (key no longer current at entry N)")
            else:
                F.add("head_unverifiable", at)


# ---------------------------------------------------------------- CLI
def load_key(path):
    if path is None:
        return None
    with open(path, "rb") as f:
        raw = f.read()
    if raw.endswith(b"\n"):
        raw = raw[:-1]
    k = b64decode_strict(raw)
    if k is None or len(k) != 32:
        sys.exit("error: %s is not the base64 of 32 bytes" % path)
    return k


def no_verdict(msg, as_json):
    """Error, no verdict; a four-level tool reports UNKNOWN (exit 3)."""
    if as_json:
        print(json.dumps({"verdict": None, "error": msg}))
    print("error: " + msg, file=sys.stderr)
    return 3


def main(argv):
    args = list(argv)
    as_json = "--json" in args
    args = [a for a in args if a != "--json"]
    head_files = []
    while "--head" in args:
        i = args.index("--head")
        head_files.append(args[i + 1])
        del args[i:i + 2]
    keyfile = None
    for flag in ("--key", "--anchor"):
        if flag in args:
            i = args.index(flag)
            keyfile = args[i + 1]
            del args[i:i + 2]
    if not args or args[0] not in ("bundle", "ledger"):
        sys.exit(__doc__)
    F = Findings()
    K = load_key(keyfile)
    if args[0] == "bundle":
        if head_files:
            sys.exit("error: head statements are only checked in ledger mode")
        bundle_procedure(args[1], K, F)
    else:
        heads = []
        for i, hf in enumerate(head_files, 1):
            try:
                with open(hf, "rb") as f:
                    raw = f.read()
            except OSError as e:
                return no_verdict("cannot read head statement %s: %s" % (hf, e), as_json)
            heads.append(("head %d (%s)" % (i, os.path.basename(hf)), raw))
        try:
            ledger_mode(args[1], args[2], K, F, heads)
        except VerifyError as e:
            return no_verdict(str(e), as_json)
    verdict = F.verdict()
    if as_json:
        print(json.dumps({"verdict": verdict, "findings": F.codes(),
                          "detail": [{"code": c, "at": w} for c, w in F.items]}))
    else:
        print(verdict)
        for c, w in F.items:
            print("  %s %s" % (c, w))
    return {"OK": 0, "WARNING": 1, "CRITICAL": 2, "UNKNOWN": 3}[verdict]


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
