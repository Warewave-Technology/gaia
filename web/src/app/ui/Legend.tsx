import { useT } from '../../i18n'
import s from './Legend.module.css'

/** What the marks of the proof bars and history strips mean. */
export function Legend() {
  const t = useT().legend
  return (
    <div className={s.legend} aria-hidden="true">
      <span><svg width="26" height="10"><rect className={s.bar} x="0" y="2.5" width="26" height="5" /></svg>{t.proven}</span>
      <span><svg width="10" height="10"><path className={s.notch} d="M1 1 L9 1 L5 7 Z" /></svg>{t.newest}</span>
      <span><svg width="6" height="12"><rect className={s.tgt} x="2" y="0" width="2" height="12" /></svg>{t.target}</span>
      <span className={s.sep} />
      <span><svg width="10" height="12"><line className={s.bk} x1="5" y1="1" x2="5" y2="11" /></svg>{t.backup}</span>
      <span><svg width="10" height="10"><circle className={s.tok} cx="5" cy="5" r="3" /></svg>{t.passed}</span>
      <span><svg width="10" height="10"><path className={s.tf} d="M2 2 l6 6 m0 -6 l-6 6" /></svg>{t.failed}</span>
      <span><svg width="10" height="10"><circle className={s.tun} cx="5" cy="5" r="3" /></svg>{t.unknown}</span>
      <span><svg width="14" height="14"><circle className={s.ret} cx="7" cy="7" r="5.5" /></svg>{t.ret}</span>
      <span><svg width="10" height="10"><rect className={s.dmg} x="1.5" y="1.5" width="7" height="7" /></svg>{t.damaged}</span>
    </div>
  )
}
