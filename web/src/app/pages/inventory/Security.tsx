import { useState, type ReactNode } from 'react'
import { Link } from 'react-router'
import { useT } from '../../../i18n'
import { dur } from '../../../lib/duration'
import { serverTime, type Fmt } from '../../../lib/time'
import { useFilters } from '../../../lib/url'
import { roles } from '../../access'
import { change, useSessions, useSettings, type Session, type SettingsUpdate, type SettingsView } from '../../api/inventory'
import type { User } from '../../api/session'
import { useSystem } from '../../api/views'
import { Failed, Loading } from '../../layout/States'
import { Button, by, DataTable, PageHead, SectionHead, Seg, StatusChip, TabPanel, Tabs, TechDetails, type Column } from '../../ui'
import s from '../Inventory.module.css'
import { Confirm, Fixed, useAction } from './common'
import { onlyWhenWeaker, stepUpName, stepUpOrder } from './words'

type Draft = Omit<SettingsUpdate, 'idle_timeout_min' | 'absolute_timeout_h'> & {
  idle_timeout_min: string
  absolute_timeout_h: string
  groups: [string, string][]
  values: string
}

function draftOf(v: SettingsView): Draft {
  return {
    oidc: v.oidc, idle_timeout_min: String(v.idle_timeout_min), absolute_timeout_h: String(v.absolute_timeout_h),
    password_max_age: v.password_max_age, online_breach_check: v.online_breach_check, breach_check_url: v.breach_check_url,
    oidc_client_secret: '', groups: Object.entries(v.oidc.roles), values: v.oidc.second_factor.values.join(', '),
  }
}

/** The draft as the PUT's body: every setting, the client secret only when a new one was typed. */
function body(d: Draft): SettingsUpdate {
  const { groups, values, oidc_client_secret, ...rest } = d
  return {
    ...rest,
    idle_timeout_min: Number(d.idle_timeout_min),
    absolute_timeout_h: Number(d.absolute_timeout_h),
    oidc: {
      ...d.oidc,
      roles: Object.fromEntries(groups.filter(([g]) => g.trim()).map(([g, r]) => [g.trim(), r])),
      second_factor: { ...d.oidc.second_factor, values: values.split(',').map((x) => x.trim()).filter(Boolean) },
    },
    ...(oidc_client_secret ? { oidc_client_secret } : {}),
  }
}

/**
 * Sign-in and security (docs/design/v2/Security.dc.html, v3 "Sign-in and security"; admins): ADR-008 as settings.
 * What ADR-008 leaves to choose has a control and is saved with one PUT through a step-up; what it fixes is listed
 * with its value, labelled "fixed", and has no control. The signed-in sessions are the second tab, a data-table.
 */
export function Security({ me }: { me: User }) {
  const t = useT().security
  const q = useSettings(true)
  const sessions = useSessions(true)
  const sys = useSystem().data
  const [f, setF] = useFilters(VIEW)
  const [gen, setGen] = useState(0)
  // the settings' save outlives the form: after a save the form starts again from what the server now holds
  const act = useAction()
  if (q.isPending || !sys) return <Loading />
  if (q.isError) return <Failed error={q.error} onRetry={() => q.refetch()} />
  const fmt = serverTime(sys.clock)
  const v = q.data
  const view: View = (VIEWS as readonly string[]).includes(f.view) ? (f.view as View) : 'sso'
  return (
    <>
      <PageHead
        question={t.question}
        answer={t.answerLine(v.oidc.enabled, dur(v.idle_timeout_min * 60), dur(v.absolute_timeout_h * 3600))}
        lede={<p>{t.lede} <Fixed /> {t.ledeEnd}</p>}
      />
      <div className={s.sec}>
        <Tabs label={t.title} value={view} onChange={(k) => setF({ view: k === 'sso' ? '' : k })}
          tabs={VIEWS.map((k) => ({ key: k, label: t.views[k], count: k === 'signed-in' ? sessions.data?.length : undefined }))} />
        <TabPanel id={view}>
          {/* the settings stay mounted across their tabs, so a draft survives a look at another tab */}
          <Settings key={gen} view={view} v={v} fmt={fmt} act={act} onSaved={() => setGen((g) => g + 1)} />
          {view === 'signed-in' && <Sessions me={me} fmt={fmt} q={sessions} />}
        </TabPanel>
      </div>
    </>
  )
}

const VIEW = ['view'] as const
const VIEWS = ['sso', 'local', 'sessions', 'step-up', 'signed-in'] as const
type View = (typeof VIEWS)[number]

function Row({ term, hint, children }: { term: string; hint?: string; children: ReactNode }) {
  return (
    <div className={s.row}>
      <dt>{term}{hint && <span>{hint}</span>}</dt>
      <dd>{children}</dd>
    </div>
  )
}

function Settings({ view, v, fmt, act, onSaved }: { view: View; v: SettingsView; fmt: Fmt; act: ReturnType<typeof useAction>; onSaved: () => void }) {
  const t = useT().security
  const ti = useT().inventory
  const [d, setD] = useState<Draft>(() => draftOf(v))
  const dirty = JSON.stringify(body(d)) !== JSON.stringify(body(draftOf(v)))
  const oidc = (patch: Partial<Draft['oidc']>) => setD((o) => ({ ...o, oidc: { ...o.oidc, ...patch } }))
  const sf = (patch: Partial<Draft['oidc']['second_factor']>) => oidc({ second_factor: { ...d.oidc.second_factor, ...patch } })
  const fx = v.fixed
  const st = v.oidc_status
  const on = d.oidc.enabled
  async function save() {
    const r = await act.run('save', 'PUT', '/api/v1/settings/signin', (h) => change.putSettings(body(d), h), () => t.save.saved)
    if (r) onSaved()
  }
  return (
    <>
      {view === 'sso' && <section className={s.sec} aria-labelledby="sso">
        <SectionHead id="sso" title={t.oidc.title}>
          <Seg label={t.oidc.title} value={on ? 'on' : 'off'} options={[['on', t.on], ['off', t.off]]} onChange={(x) => oidc({ enabled: x === 'on' })} />
          {v.oidc.enabled && (
            <span className={s.inline}>
              <StatusChip
                status={!st.checked_at ? 'UNKNOWN' : st.ok ? 'OK' : 'CRITICAL'}
                word={!st.checked_at ? t.oidc.word.notChecked : st.ok ? t.oidc.word.connected : t.oidc.word.failing}
                size="sm"
              />
              {st.checked_at && <span className={s.sub}>{t.oidc.checked(fmt.at(st.checked_at))}</span>}
            </span>
          )}
        </SectionHead>
        {v.oidc.enabled && !st.ok && st.error && <TechDetails summary={t.oidc.error}>{st.error}</TechDetails>}
        {!on ? <p className={s.sub}>{t.oidc.off}</p> : (
          <dl className={s.dl}>
            <Row term={t.oidc.name} hint={t.oidc.nameHint}>
              <span className={s.inline}><input value={d.oidc.name} onChange={(e) => oidc({ name: e.target.value })} aria-label={t.oidc.name} /></span>
            </Row>
            <Row term={t.oidc.issuer}>
              <span className={s.inline}><input size={48} value={d.oidc.issuer} onChange={(e) => oidc({ issuer: e.target.value })} aria-label={t.oidc.issuer} placeholder="https://" /></span>
            </Row>
            <Row term={t.oidc.client} hint={t.oidc.clientHint}>
              <span className={s.inline}><input value={d.oidc.client_id} onChange={(e) => oidc({ client_id: e.target.value })} aria-label={t.oidc.client} /></span>
              <span className={s.sub}>{t.oidc.secret}: {v.oidc_client_secret_set ? t.oidc.secretSet : t.oidc.secretUnset}</span>
              <span className={s.inline}>
                <input type="password" autoComplete="off" value={d.oidc_client_secret ?? ''} placeholder={t.oidc.secretNew} aria-label={t.oidc.secretNew}
                  onChange={(e) => setD((o) => ({ ...o, oidc_client_secret: e.target.value }))} />
              </span>
            </Row>
            <Row term={t.oidc.roles} hint={t.oidc.rolesHint(d.oidc.groups_claim)}>
              <span className={s.inline}>
                <span className={s.sub}>{t.oidc.claim}</span>
                <input value={d.oidc.groups_claim} onChange={(e) => oidc({ groups_claim: e.target.value })} aria-label={t.oidc.claim} />
              </span>
              {d.groups.map(([g, r], i) => (
                <span key={i} className={s.inline}>
                  <input value={g} aria-label={t.oidc.group} placeholder={t.oidc.group}
                    onChange={(e) => setD((o) => ({ ...o, groups: o.groups.map((x, j) => (j === i ? [e.target.value, x[1]] : x)) }))} />
                  →
                  <select value={r} aria-label={t.oidc.role}
                    onChange={(e) => setD((o) => ({ ...o, groups: o.groups.map((x, j) => (j === i ? [x[0], e.target.value] : x)) }))}>
                    {roles.map((x) => <option key={x} value={x}>{x}</option>)}
                  </select>
                  <button type="button" className={s.link} onClick={() => setD((o) => ({ ...o, groups: o.groups.filter((_, j) => j !== i) }))}>{t.oidc.removeGroup}</button>
                </span>
              ))}
              <button type="button" className={s.link} onClick={() => setD((o) => ({ ...o, groups: [...o.groups, ['', 'operator']] }))}>{t.oidc.addGroup}</button>
              <span className={s.sub}>{t.oidc.rolesNote(fx.oidc_refresh_min)}</span>
            </Row>
            <Row term={t.oidc.factor} hint={t.oidc.factorHint}>
              <label className={s.check}>
                <input type="checkbox" checked={d.oidc.second_factor.required} onChange={(e) => sf({ required: e.target.checked })} />
                {t.oidc.factorRequired}
              </label>
              {d.oidc.second_factor.required ? (
                <>
                  <span className={s.inline}>
                    <span className={s.sub}>{t.oidc.factorClaim}</span>
                    <select value={d.oidc.second_factor.claim} onChange={(e) => sf({ claim: e.target.value })} aria-label={t.oidc.factorClaim}>
                      <option value="acr">acr</option>
                      <option value="amr">amr</option>
                    </select>
                    <span className={s.sub}>{t.oidc.factorValues}</span>
                    <input value={d.values} onChange={(e) => setD((o) => ({ ...o, values: e.target.value }))} aria-label={t.oidc.factorValues} />
                  </span>
                  <span className={s.sub}>{t.oidc.factorNote}</span>
                </>
              ) : <span className={s.sub}>{t.oidc.factorOff}</span>}
            </Row>
            {v.oidc.enabled && st.checked_at && (
              <Row term={t.oidc.device}><span className={s.sub}>{t.oidc.deviceText(st.device_grant)}</span></Row>
            )}
          </dl>
        )}
      </section>}

      {view === 'local' && <section className={s.sec} aria-labelledby="local">
        <SectionHead id="local" title={t.local.title} />
        <dl className={s.dl}>
          <Row term={t.local.who} hint={on ? t.local.whoSsoHint : t.local.whoAllHint}><span className={s.val}>{on ? t.local.whoSso : t.local.whoAll}</span></Row>
          <Row term={t.local.breakGlass}>
            <span className={s.val}>{t.local.breakGlassVal(v.break_glass_admins)}</span>
            <span className={s.sub}><Fixed />{t.local.breakGlassNote}</span>
          </Row>
          <Row term={t.local.factor}>
            <span className={s.val}>{t.local.factorVal}{fx.second_factor_required && <Fixed />}</span>
            <span className={s.sub}>{t.local.factorNote(fx.admin_factor)}</span>
          </Row>
          <Row term={t.local.password}>
            <span className={s.val}>{t.local.passwordVal(fx.password_min_length, fx.password_max_length, fx.password_history)}<Fixed /></span>
            <span className={s.sub}>{t.local.passwordNote(v.password_blocklist.entries)}</span>
            <TechDetails summary={t.local.blocklist}>{v.password_blocklist.version}</TechDetails>
          </Row>
          <Row term={t.local.breach} hint={t.local.breachHint}>
            <Seg label={t.local.breach} value={d.online_breach_check ? 'on' : 'off'} options={[['on', t.on], ['off', t.off]]}
              onChange={(x) => setD((o) => ({ ...o, online_breach_check: x === 'on' }))} />
            {d.online_breach_check && (
              <span className={s.inline}>
                <span className={s.sub}>{t.local.breachUrl}</span>
                <input size={40} value={d.breach_check_url} onChange={(e) => setD((o) => ({ ...o, breach_check_url: e.target.value }))} aria-label={t.local.breachUrl} />
              </span>
            )}
          </Row>
          <Row term={t.local.rotation} hint={t.local.rotationHint}>
            <Seg label={t.local.rotation} value={d.password_max_age} options={(['none', '90d', '180d'] as const).map((x) => [x, t.local.rot[x]] as const)}
              onChange={(x) => setD((o) => ({ ...o, password_max_age: x }))} />
            <span className={s.sub}>{t.local.rotNote(d.password_max_age)}</span>
          </Row>
          <Row term={t.local.failures}>
            <span className={s.val}>{t.local.failuresVal(fx.slow_down_from_failure, fx.pause_after_failures, fx.pause_window_min, fx.pause_min)}<Fixed /></span>
            <span className={s.sub}>{t.local.failuresNote}</span>
          </Row>
          <Row term={t.local.auditors}>
            <span className={s.val}>{t.local.auditorsVal(fx.auditor_max_days)}<Fixed /></span>
            <span className={s.sub}>{t.local.auditorsNote}</span>
          </Row>
          <Row term={t.local.tokens}>
            <span className={s.val}>{t.local.tokensVal(fx.api_token_max_days)}<Fixed /></span>
          </Row>
        </dl>
      </section>}

      {view === 'sessions' && <section className={s.sec} aria-labelledby="sessions-rules">
        <SectionHead id="sessions-rules" title={t.sessions.title} />
        <dl className={s.dl}>
          <Row term={t.sessions.idle} hint={t.sessions.idleRange}>
            <span className={s.inline}>
              <input type="number" min={5} max={60} value={d.idle_timeout_min} aria-label={t.sessions.idle}
                onChange={(e) => setD((o) => ({ ...o, idle_timeout_min: e.target.value }))} />
              <span className={s.sub}>{t.sessions.idleUnit}</span>
            </span>
            <span className={s.sub}>{t.sessions.idleNote}</span>
          </Row>
          <Row term={t.sessions.absolute} hint={t.sessions.absoluteRange}>
            <span className={s.inline}>
              <input type="number" min={1} max={12} value={d.absolute_timeout_h} aria-label={t.sessions.absolute}
                onChange={(e) => setD((o) => ({ ...o, absolute_timeout_h: e.target.value }))} />
              <span className={s.sub}>{t.sessions.absoluteUnit}</span>
            </span>
          </Row>
          <Row term={t.sessions.perUser}><span className={s.val}>{t.sessions.perUserVal(fx.sessions_per_user)}<Fixed /></span></Row>
          <Row term={t.sessions.cookie}>
            <span className={s.val}>{t.sessions.cookieVal}<Fixed /></span>
            <span className={s.sub}>{t.sessions.cookieNote}</span>
            <TechDetails>{fx.cookie}</TechDetails>
          </Row>
          <Row term={t.sessions.onChange}><span className={s.val}>{t.sessions.onChangeVal}<Fixed /></span></Row>
          <Row term={t.sessions.refresh}><span className={s.val}>{t.sessions.refreshVal(fx.oidc_refresh_min)}<Fixed /></span></Row>
          <Row term={t.sessions.newDevice}><span className={s.val}>{t.sessions.newDeviceVal(fx.new_device_days)}<Fixed /></span></Row>
        </dl>
      </section>}

      {view === 'step-up' && (
        <section className={s.sec} aria-labelledby="step-up">
          <SectionHead id="step-up" title={t.sessions.stepUp} hint={<><Fixed />{t.sessions.stepUpNote(fx.step_up_s)}</>} />
          <ul className={s.stepList}>
            {stepUpOrder(v.step_up_actions).map((r) => (
              <li key={r}>{stepUpName(r) ?? ti.audit.other}{onlyWhenWeaker(r) && <span>{t.sessions.whenWeaker}</span>}</li>
            ))}
          </ul>
          <p className={s.sub}>{t.sessions.stepUpHow}</p>
          <TechDetails summary={t.sessions.routes}>{v.step_up_actions.join('\n')}</TechDetails>
        </section>
      )}

      {(dirty || act.outcome) && (
        <div className={s.save}>
          {dirty && <span className={s.val}>{t.save.dirty}</span>}
          {dirty && <Button onClick={save} disabled={act.busy === 'save'}>{act.busy === 'save' ? ti.working : t.save.save}</Button>}
          {dirty && <Button variant="quiet" onClick={() => { setD(draftOf(v)); act.clear() }}>{t.save.discard}</Button>}
          {act.outcome}
        </div>
      )}
    </>
  )
}

/** "Firefox, Linux" from a User-Agent; the whole string when neither is recognised. */
function browser(ua: string) {
  const b = /Edg\//.test(ua) ? 'Edge' : /Firefox\//.test(ua) ? 'Firefox' : /Chrome\//.test(ua) ? 'Chrome' : /Safari\//.test(ua) ? 'Safari' : ''
  const os = /Android/.test(ua) ? 'Android' : /iPhone|iPad/.test(ua) ? 'iOS' : /Windows/.test(ua) ? 'Windows' : /Mac OS X/.test(ua) ? 'macOS' : /Linux/.test(ua) ? 'Linux' : ''
  return b && os ? `${b}, ${os}` : ua
}

function Sessions({ me, fmt, q }: { me: User; fmt: Fmt; q: ReturnType<typeof useSessions> }) {
  const t = useT().security
  const si = t.signedIn
  const act = useAction()
  const mine = me.session.id
  const cols: Column<Session>[] = [
    {
      key: 'user', header: si.cols.user, width: '200px', compare: by((x) => x.username),
      cell: (x) => <><span className={s.name}>{x.username}</span><span className={s.cellOf}>{x.id === mine ? si.this : x.role}</span></>,
    },
    {
      key: 'how', header: si.cols.how, title: (x) => si.how(x.method, x.kind, x.break_glass),
      cell: (x) => (
        <>
          {x.new_device && <StatusChip status="WARNING" word={si.word.newDevice} size="xs" />}
          {x.idp_state === 'idp_unreachable' && <StatusChip status="UNKNOWN" word={si.word.idp} size="xs" />}
          <span>{si.how(x.method, x.kind, x.break_glass)}</span>
        </>
      ),
    },
    {
      key: 'from', header: si.cols.from, width: '230px', title: (x) => x.user_agent,
      cell: (x) => <><span className={s.mono}>{x.remote_addr}</span><span className={s.cellOf}>{browser(x.user_agent)}</span></>,
    },
    { key: 'started', header: si.cols.started, width: '130px', compare: by((x) => x.created_at), cell: (x) => fmt.at(x.created_at) },
    { key: 'active', header: si.cols.active, width: '130px', compare: by((x) => x.last_active_at), cell: (x) => fmt.at(x.last_active_at) },
    { key: 'ends', header: si.cols.ends, width: '130px', compare: by((x) => x.absolute_at), cell: (x) => fmt.at(x.absolute_at) },
    {
      key: 'action', header: si.cols.action, width: '110px',
      cell: (x) => (x.id === mine ? null : (
        <Button variant="quiet" className={s.cellBtn} disabled={act.busy === `end/${x.id}`}
          onClick={() => act.run(`end/${x.id}`, 'DELETE', `/api/v1/sessions/${x.id}`, (h) => change.endSession(x.id, h))}>
          {si.signOut}
        </Button>
      )),
    },
  ]
  return (
    <section className={s.sec} aria-label={si.title}>
      <div className={s.toolbar}>
        {q.data && <span className={s.sub}>{si.count(q.data.length)}</span>}
        <span className={s.end}>
          <Confirm danger label={si.endOthers} question={si.endOthersConfirm} busy={act.busy === 'others'}
            onYes={() => act.run('others', 'POST', '/api/v1/sessions/end-others', (h) => change.endOthers(h), (r) => si.ended(r.ended))} />
        </span>
      </div>
      {q.isPending ? <Loading /> : q.isError ? <Failed error={q.error} onRetry={() => q.refetch()} /> : (
        <DataTable<Session>
          caption={si.caption}
          columns={cols}
          rows={q.data}
          rowKey={(x) => String(x.id)}
          rowState={(x) => (x.idp_state === 'idp_unreachable' ? 'bad' : undefined)}
          minWidth={1000}
          detail={(x) => (
            <div className={s.facts}>
              {x.new_device && <p>{si.newDevice}</p>}
              {x.idp_state === 'idp_unreachable' && <p>{si.idp}</p>}
              {x.idp_refreshed_at && <p>{si.refreshed(fmt.at(x.idp_refreshed_at))}</p>}
              <TechDetails summary={si.agent}>{x.user_agent}</TechDetails>
            </div>
          )}
        />
      )}
      {act.outcome}
      <Link to="/inventory/audit" className={s.sub}>{si.auditLink}</Link>
    </section>
  )
}
