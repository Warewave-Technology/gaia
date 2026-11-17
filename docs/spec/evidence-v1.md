# Restore Evidence Format, version 1

> **License: CC0 1.0 Universal.** To the extent possible under law, the authors have waived all copyright and
> related or neighboring rights to this specification text and to the conformance vectors in `vectors/`. You may
> implement, copy, modify and distribute it, for any purpose, without asking. See
> <https://creativecommons.org/publicdomain/zero/1.0/>. (The Gaia software that implements it is licensed
> separately, under AGPL-3.0-or-later.)

Status: version 1, 2026-11-16. Addition 2026-11-17: head statements (§6.8, §8.6) and their finding codes. The
addition is optional for a verifier that is given no head statements, and it changes nothing for one that is: every
document and vector of 2026-11-16 verifies as before. Reference implementation: Gaia's `internal/evidence`; commands
`gaia evidence verify` and `gaia evidence head`.

## 1. Purpose

A **bundle** is a directory of files that show what happened when a backup was restored and tested: logs,
screenshots, check results, timings. A **ledger** is an append-only list of the bundles one installation has
produced. This format makes both tamper-evident:

- every file of a bundle is hashed into a manifest, and the manifest is signed;
- every ledger entry is signed and carries the hash of the entry before it;
- the ledger names the keys that sign it, starting from one key that the verifier already trusts;
- a **head statement**, a signed record of the ledger's last entry kept somewhere else, lets a verifier detect a
  ledger that was cut off or rewritten after it.

Anyone holding a ledger, its bundles and the trusted key can check, without trusting the system that produced
them, that no file was changed, added or removed, and that no entry was changed, removed or reordered (with the
limit in §11.1). This document says exactly how; it is meant to be enough to write a verifier without reading any
implementation.

The key words MUST, MUST NOT, SHOULD and MAY are used as in RFC 2119.

## 2. Building blocks

| Name | Definition |
|---|---|
| hash | SHA-256 (FIPS 180-4). Written as **hex64**: 64 lowercase hexadecimal digits (`0-9a-f`). |
| signature | Ed25519 (RFC 8032, pure Ed25519, no prehash, no context). Public keys are 32 bytes, signatures 64 bytes. A verifier MUST reject a signature whose S is not less than the group order L, and checks [S]B = R + [k]A by comparing the encoding of [S]B − [k]A with R byte for byte (cofactorless, RFC 8032 §5.1.7). The vectors contain only signatures made by standard signers, on which the common variants of these rules agree. |
| base64 | RFC 4648 §4: standard alphabet, with `=` padding, no line breaks. Decoders MUST reject anything else (non-alphabet characters, missing or extra padding, non-zero trailing bits). |
| JSON | RFC 8259, encoded as UTF-8. Additional rules for every JSON text in this format: it is exactly one value; an object MUST NOT repeat a member name, at any depth; **integer** means a number written as `0` or as a non-zero digit followed by at most 15 digits (no sign, fraction, exponent or leading zero). Unknown members MUST be ignored unless a section says otherwise. |
| time | An RFC 3339 date-time in UTC that matches `^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(\.[0-9]{1,9})?Z$` and is a valid date and time, with seconds 00–59 (no leap second). Times are informational; verification does not depend on clocks. |
| key_id | The hex64 of the hash of the 32-byte raw Ed25519 public key. |
| zero hash | hex64 of 64 `0` digits. |

## 3. Keys

- An installation signs with one Ed25519 key at a time: the **current key**.
- The verifier needs one key from outside the data: the **trust anchor**. It is the ledger's first key (§6.4).
  It is given to a verifier as the base64 of the 32 raw bytes; whitespace before and after it (such as a final
  line feed) is ignored. Gaia also accepts an X.509 SubjectPublicKeyInfo PEM block, as written by
  `gaia evidence keygen`.
- How the trust anchor reaches the verifier (an audit file, a printed key_id, the operator's own records) is
  outside this format. A verifier MUST NOT take the trust anchor from the ledger it verifies.
- A key is replaced by a **rotation** entry (§6.5): the current key signs an entry that names the next key.

## 4. Layout

```
<bundles directory>/            the bundles of one ledger
    <bundle_id>/                one bundle: a directory named by its bundle_id
        manifest.json           §5.2
        manifest.sig            §5.4
        <any files and subdirectories>
<ledger file>                   §6, one per installation (any file name)
```

A **bundle_id** is 1 to 255 characters from `A-Z a-z 0-9 . _ -`, and is neither `.` nor `..`.

## 5. Bundles

### 5.1 Content

The content of a bundle is every **regular file** below its directory, at any depth, except `manifest.json` and
`manifest.sig` directly in it. Directories only give files their paths: empty directories are not content.
Anything that is neither a directory nor a regular file (a symbolic link, a device, a socket, a FIFO) MUST NOT
be in a bundle. Symbolic links are never followed.

A file's **path** is its location relative to the bundle directory, with `/` between segments, as bytes of UTF-8.

### 5.2 manifest.json

A JSON object with these members:

| Member | Type | Value |
|---|---|---|
| `format` | string | `"restore-evidence"` |
| `format_version` | integer | `1` |
| `bundle_id` | string | the bundle_id (§4) |
| `created` | string | a time (§2): when the bundle was sealed |
| `key_id` | string | hex64: the key_id of the key that signed `manifest.json` |
| `subject` | object | what the evidence is about, for people (e.g. `{"vm": "web1", "verdict": "OK"}`); every member's value is a string; it MAY be empty |
| `files` | array | one object per content file (§5.1), ordered by `path`, strictly ascending by byte-wise comparison of the UTF-8 paths |

Each element of `files`:

| Member | Type | Value |
|---|---|---|
| `path` | string | the file's path (§5.3) |
| `sha256` | string | hex64: the hash of the file's bytes |
| `bytes` | integer | the file's size in bytes |

### 5.3 Paths

A path is valid when all of these hold:

- it is valid UTF-8, 1 to 4096 bytes long, and contains no `\` and no NUL;
- split at `/`, no segment is empty, `.` or `..` (so it does not start or end with `/`);
- it is not `manifest.json` or `manifest.sig` (these two names are reserved at the top level only;
  `logs/manifest.json` is an ordinary path).

### 5.4 manifest.sig

The base64 of the Ed25519 signature, made with the key named by `key_id`, of **the exact bytes of
`manifest.json` as stored**. The base64 text MAY be followed by one line feed (0x0A) and by nothing else.
`manifest.json` and `manifest.sig` MUST be regular files; a symbolic link in their place is not followed and
counts as missing.

The signed bytes are the file's bytes. No canonical form is computed: a verifier hashes and checks the bytes it
reads, and a producer may lay out `manifest.json` as it likes (Gaia writes it indented, with a final line feed).

### 5.5 The bundle hash

The **bundle hash** is the hash of the bytes of `manifest.json`. The ledger records it (§6.6). Because the
manifest lists every file's hash, the bundle hash commits to the whole bundle.

### 5.6 Producing a bundle (informative)

1. Write the files.
2. Hash every content file.
3. Write `manifest.json` with the files sorted by path.
4. Sign its bytes with the current key and write `manifest.sig`.
5. Append a bundle entry (§6.6) with the bundle hash to the ledger.

Nothing in the directory may change afterwards.

## 6. The ledger

### 6.1 File

- The ledger is a sequence of **lines**, each terminated by one line feed (0x0A).
- The file is either empty or ends with a line feed.
- Line *n* (counting from 1) is entry *n*.
- A ledger is only ever appended to.

### 6.2 Line

Each line is a JSON object with two string members:

| Member | Value |
|---|---|
| `payload` | a JSON text (§6.3), carried as a JSON string |
| `sig` | base64 of the Ed25519 signature of the payload bytes, by the key current when the entry was written |

The **payload bytes** are the UTF-8 encoding of the `payload` string's value after JSON string decoding. They are
what is signed and hashed. Every JSON library returns them unchanged; no canonical form is computed.

- A `\u` escape of a UTF-16 surrogate that is not part of a high-then-low pair has no UTF-8 encoding, so
  libraries disagree on what it decodes to. Such a line is malformed (§8.2 L2 step 2).
- JSON whitespace around the object, including a carriage return before the line feed, is allowed; it is not part
  of the payload bytes.

The **entry hash** of entry *n* is the hash of its payload bytes.

### 6.3 Payload

The payload is a JSON object. Every entry has these members:

| Member | Type | Value |
|---|---|---|
| `format` | string | `"restore-evidence-ledger"` |
| `format_version` | integer | `1` |
| `seq` | integer | *n*, the line number (≥ 1) |
| `time` | string | a time (§2): when the entry was written |
| `type` | string | `"key"` or `"bundle"` |
| `key_id` | string | hex64: the key_id of the key that signed this entry |
| `prev` | string | hex64: the entry hash of entry *n − 1*; the zero hash for entry 1 |

Members for `type` `"key"`:

| Member | Type | Value |
|---|---|---|
| `event` | string | `"genesis"` or `"rotate"` |
| `public_key` | string | base64 of a 32-byte Ed25519 public key: for genesis the signing key itself, for rotate the next key |
| `new_key_id` | string | rotate only: hex64, the key_id of `public_key` |

Members for `type` `"bundle"`:

| Member | Type | Value |
|---|---|---|
| `bundle_id` | string | the bundle_id (§4) |
| `bundle_sha256` | string | hex64: the bundle hash (§5.5) |
| `subject` | string | what the bundle is about, for people (e.g. a VM name) |
| `verdict` | string | the outcome the bundle records, for people (e.g. `OK`, `CRITICAL`) |

`time`, `subject` and `verdict` are informational: verification checks their syntax, not their meaning.

### 6.4 Genesis

Entry 1 is always a key entry with event `genesis`:
- `public_key` is the first signing key;
- `key_id` is that key's key_id;
- the entry is signed by that key.

This key is the trust anchor (§3). A genesis entry anywhere else is invalid.

### 6.5 Rotation

A key entry with event `rotate` hands the ledger over from the current key to the key in `public_key`:

- it is signed by the current key, and `key_id` names the current key ("the old key endorses the new key");
- `new_key_id` is the key_id of `public_key`, and the new key is not the current key;
- from the next entry on, the new key is the current key: entries and bundles MUST be signed by it, and the old
  key is no longer accepted.

Entries signed before a rotation stay valid.

### 6.6 Bundle entries

A bundle entry records that the bundle `bundle_id`, whose bundle hash is `bundle_sha256`, was sealed by the
current key. A bundle_id appears in at most one entry. The bundle's directory is `<bundles directory>/<bundle_id>`.

### 6.7 Appending (informative)

A writer:

1. Takes an exclusive lock on the ledger and reads it.
2. Refuses to append if the ledger does not verify (§8.2, without bundles).
3. Builds the payload. It uses `seq` = number of lines + 1 and `prev` = the last entry hash; a new ledger starts
   with a genesis entry.
4. Signs the payload bytes.
5. Writes the line and flushes it to stable storage.

### 6.8 Head statements

A head statement records, outside the ledger, which entry was the ledger's last at a given time. It has the form of
a ledger line (§6.2): a JSON object with a string `payload` and a string `sig`, where `sig` is the base64 of the
Ed25519 signature of the payload bytes. It is not a line of the ledger and is never appended to it. The payload is a
JSON object:

| Member | Type | Value |
|---|---|---|
| `format` | string | `"restore-evidence-head"` |
| `format_version` | integer | `1` |
| `time` | string | a time (§2): when the statement was made |
| `key_id` | string | hex64: the key_id of the key that signed the statement |
| `seq` | integer | *n* ≥ 1: the number of entries the ledger had |
| `entry_sha256` | string | hex64: the entry hash of entry *n* |

The statement is signed by the key that was current after entry *n* (§8.2 step 11): the key that would sign
entry *n* + 1. A producer makes it under the same lock as an append (§6.7) and sends it where the producer cannot
change it later, for example in a report mailed to other people (§11.1). A file that holds a head statement MAY
end with a line feed.

## 7. Versions

- `format` and `format_version` identify this specification. A later version will use a different
  `format_version`, and documents of version 1 stay verifiable by version 1's rules.
- A verifier that meets a `format_version` it does not implement MUST NOT guess: it reports
  `format_unsupported`, which is UNKNOWN (§8.3).
- Documents written before this specification (version 0) are described in appendix A.

## 8. Verification

### 8.1 Results

A verification produces a list of **findings**. Each finding has:
- a **code** from §8.3;
- a severity, CRITICAL or UNKNOWN, fixed by the code;
- an optional location: a ledger line, a bundle_id, a path.

The **verdict** is:

| Verdict | When |
|---|---|
| `CRITICAL` | at least one finding is CRITICAL: something was checked and is wrong |
| `UNKNOWN` | no finding is CRITICAL and at least one is UNKNOWN: something could not be checked |
| `OK` | there are no findings |

An implementation MUST NOT report `OK` for anything it did not check. The verdicts are a subset of a four-level
model (OK, WARNING, CRITICAL, UNKNOWN) shared with other tools; WARNING is not used by this version.

There are two modes:
- **bundle mode** (§8.4) checks one bundle against the key that should have signed it;
- **ledger mode** (§8.2) checks a ledger, every bundle it names, and the bundles directory.

### 8.2 Ledger mode

**Input:** the ledger file; the bundles directory *D*; the trust anchor *A* (it may be absent); and a list of head
statements (§6.8), which may be empty. With an empty list, step L5 adds nothing.

The procedure keeps:
- the **current key** *K*;
- the **previous entry hash** *P*;
- the list of **bundle references**: a bundle_id with its recorded bundle hash, its line and the key current when
  the entry was read.

*"Stop"* means: end step L2 and go to step L3.

**L1 — read the ledger.**
- If the file does not exist: report `ledger_missing` and go to L4 (and then L5, with no entries read).
- If it exists but cannot be read (for example, it is a directory): verification ends with an error and no
  verdict. A tool that reports four-level verdicts reports UNKNOWN.
- If it is empty: report `ledger_empty` and go to L4 (and then L5, with no entries read).
- If its first line is a version 0 ledger (appendix A), use appendix A instead of this procedure, and report
  `format_unsupported` once for each head statement: they need a version 1 ledger. An implementation without
  appendix A reports `format_unsupported` and stops.

**L2 — entries.** Split the file at line feeds. For each line *n* = 1, 2, … in order:

1. *Truncation.* If the file does not end with a line feed and this is its last line, report `entry_malformed`
   and stop.
2. *Line.* Parse the line as a JSON object (§2) with a string `payload` and a string `sig`. An empty line, or
   an unpaired surrogate escape anywhere in the line (§6.2), also fails this step. Otherwise report
   `entry_malformed` and stop.
3. *Payload.* Parse the payload bytes (§6.2) as a JSON object (§2). Otherwise report `entry_malformed` and stop.
4. *Format.*
   - `format` must be the string `"restore-evidence-ledger"` and `format_version` an integer; otherwise report
     `entry_malformed` and stop.
   - If `format_version` is not `1`, report `format_unsupported` and stop.
5. *Members.* Check every member of §6.3 that applies, with its type and syntax:
   - `seq` is at least 1;
   - `time` is a time;
   - `type` is `key` or `bundle`;
   - hex64 values are hex64;
   - `event` is `genesis` or `rotate`;
   - `new_key_id` is present for `rotate`;
   - `bundle_id` is a bundle_id.

   Here `public_key` only has to be a string; whether it decodes to a key is checked in steps 8 and 10. Members of
   the other entry type (e.g. `new_key_id` on a genesis entry) are ignored.

   If anything fails, report `entry_malformed` and stop. Let *H* be the entry hash. The checks below each report
   their finding and continue.
6. *Sequence.* If `seq` ≠ *n*: `seq_invalid`.
7. *Chain.* If `prev` ≠ *P* (*P* is the zero hash for *n* = 1): `chain_broken`.
8. *Key.*
   - If *n* = 1:
     - The entry must be a key entry with event `genesis`; otherwise report `anchor_mismatch` and stop.
     - `public_key` must be the base64 of 32 bytes whose key_id is `key_id`; otherwise report
       `key_entry_invalid` and stop.
     - If *A* is absent, report `anchor_missing` and let *A* be this key. If *A* is present and is not this
       key, report `anchor_mismatch` and stop.
     - Let *K* = *A*.
   - If *n* > 1 and `key_id` ≠ key_id(*K*): `signer_invalid`.
9. *Signature.* `sig` must be the base64 of 64 bytes that is a valid signature by *K* of the payload bytes;
   otherwise report `entry_signature_invalid`. (*K* is the current key, whatever `key_id` claims.)
10. *Type.*
    - A key entry with event `genesis` (here *n* > 1 always): `key_entry_invalid`.
    - A key entry with event `rotate`: `public_key` must be the base64 of 32 bytes, its key_id must be
      `new_key_id`, and it must not be *K*. Otherwise report `key_entry_invalid`.
    - A bundle entry:
      - If an earlier bundle reference has the same bundle_id, report `bundle_duplicate`.
      - In all cases add the reference (bundle_id, `bundle_sha256`, *n*, *K*).
11. *Next.*
    - If the entry is a rotation and steps 6–10 reported nothing for it, the rotation takes effect: let *K* be its
      `public_key`.
    - Let *P* = *H*.

**L3 — bundles.** For each bundle reference, in order of the lines that made them:
1. If *D*/*bundle_id* is not a directory, report `bundle_missing`. A symbolic link is not a directory and is not
   followed.
2. Otherwise check it with the bundle procedure (§8.4) and the reference's key as *K*. If *A* was absent, that
   key is the one L2 took from the genesis entry.
3. Then:
   - If `manifest.json` could be read (whether or not it parsed), its hash must be the reference's bundle hash;
     otherwise report `bundle_hash_mismatch`.
   - If step 3 of §8.4 succeeded (every member valid), the manifest's `bundle_id` must be the reference's
     bundle_id; otherwise report `bundle_id_mismatch`.
   - A version 0 bundle in this ledger is reported as `format_unsupported` instead of being checked; neither
     comparison above is made.

**L4 — unlisted bundles.** Every entry of *D* that is a directory and whose name is not the bundle_id of a bundle
reference is reported as `bundle_unlisted`. Entries of *D* that are not directories (symbolic links included)
are ignored. A missing *D*
adds nothing here (L3 reports the missing bundles); a *D* that exists but cannot be read is reported as
`bundle_missing`.

**L5 — head statements.** See §8.6.

The verdict combines all findings of L1–L5. `anchor_missing` is reported once, in L2, not again per bundle.

### 8.3 Finding codes

| Code | Severity | Meaning |
|---|---|---|
| `manifest_missing` | CRITICAL | `manifest.json` cannot be read |
| `manifest_malformed` | CRITICAL | `manifest.json` is not a JSON object by §2, or a member of §5.2 is missing or has the wrong type or syntax |
| `format_unsupported` | UNKNOWN | the `format` or `format_version` of a manifest, or the `format_version` of an entry, is not one this verifier implements |
| `key_mismatch` | CRITICAL | the manifest's `key_id` is not the key that should have signed the bundle |
| `signature_missing` | CRITICAL | `manifest.sig` cannot be read |
| `signature_malformed` | CRITICAL | `manifest.sig` is not the base64 of 64 bytes (§5.4) |
| `signature_invalid` | CRITICAL | `manifest.sig` is not a valid signature of `manifest.json`'s bytes |
| `path_invalid` | CRITICAL | a `files` path is not valid (§5.3), or not strictly greater than the valid path before it |
| `file_missing` | CRITICAL | a listed file is not a regular file in the bundle |
| `file_modified` | CRITICAL | a listed file's hash or size differs from the manifest |
| `file_unlisted` | CRITICAL | a regular file in the bundle is not listed |
| `file_type_invalid` | CRITICAL | the bundle holds something that is neither a directory nor a regular file |
| `anchor_missing` | UNKNOWN | no trust anchor (ledger mode) or no key (bundle mode) was given |
| `ledger_missing` | CRITICAL | the ledger file does not exist |
| `ledger_empty` | CRITICAL | the ledger file is empty |
| `entry_malformed` | CRITICAL | a line or payload breaks §6.1–§6.3 (truncated last line, not JSON, duplicate member, missing or wrong member) |
| `seq_invalid` | CRITICAL | an entry's `seq` is not its line number |
| `chain_broken` | CRITICAL | an entry's `prev` is not the entry hash of the line before |
| `entry_signature_invalid` | CRITICAL | an entry's `sig` is not the current key's signature of its payload bytes |
| `anchor_mismatch` | CRITICAL | the first entry is not a genesis entry, or its key is not the trust anchor |
| `signer_invalid` | CRITICAL | an entry's `key_id` is not the current key |
| `key_entry_invalid` | CRITICAL | a genesis entry after line 1, or a key entry whose `public_key` does not match its key_id or does not change the key |
| `bundle_missing` | CRITICAL | an entry names a bundle whose directory does not exist |
| `bundle_unlisted` | CRITICAL | a bundle directory has no entry |
| `bundle_duplicate` | CRITICAL | a bundle_id appears in more than one entry |
| `bundle_hash_mismatch` | CRITICAL | a bundle's `manifest.json` is not the one its entry recorded |
| `bundle_id_mismatch` | CRITICAL | a bundle's manifest names another bundle_id than its entry and directory |
| `head_malformed` | CRITICAL | a head statement breaks §6.8 (not JSON, duplicate member, missing or wrong member) |
| `head_signature_invalid` | CRITICAL | a head statement is not signed by the key it must be signed by (§8.6) |
| `head_mismatch` | CRITICAL | a head statement names an entry the ledger has, but with another entry hash: the ledger was rewritten |
| `ledger_truncated` | CRITICAL | a validly signed head statement names an entry beyond the ledger's last: the ledger was cut off |
| `head_unverifiable` | UNKNOWN | a head statement names an entry beyond the ledger's last, signed by a key the ledger does not lead to |

A verifier MAY report a finding more than once (e.g. one per file) and MAY add detail and locations. Conformance
(§10) compares verdicts and the *set* of codes.

### 8.4 Bundle mode, and the bundle procedure

**Input:** a bundle directory; the key *K* that should have signed it (in bundle mode it may be absent).

1. Read `manifest.json`. If it cannot be read or is not a regular file, report `manifest_missing` and end.
2. Parse it as a JSON object (§2); otherwise report `manifest_malformed` and end. If its `format` is the version 0
   string (appendix A), use appendix A instead.
3. Check the members of §5.2:
   - If `format` is not a string, report `manifest_malformed` and end.
   - If it is not `"restore-evidence"`, or `format_version` is an integer other than `1`, report
     `format_unsupported` and end.
   - Any other missing member, wrong type or wrong syntax (a missing or non-integer `format_version` included):
     report `manifest_malformed` and end. The paths' rules (§5.3, order) are not checked here but in step 5.
4. Signature:
   - If *K* is absent, report `anchor_missing`; the signature is not checked.
   - Otherwise, if `key_id` ≠ key_id(*K*), report `key_mismatch`; the signature is not checked.
   - Otherwise:
     - `manifest.sig` must be a readable regular file (`signature_missing`) and decode per §5.4
       (`signature_malformed`).
     - It must verify against the bytes read in step 1 with *K* (`signature_invalid`).
5. Paths, in the order of `files`:
   - A path that is not valid (§5.3) is reported as `path_invalid` and ignored afterwards.
   - A valid path that is not strictly greater than the previous path that was valid by §5.3 is reported as
     `path_invalid` too (duplicates included). Such a path still counts as listed in steps 7 and 8.
6. Content: collect the content of the bundle (§5.1). Everything that is neither a directory nor a regular file is
   reported as `file_type_invalid`.
7. For every valid listed path:
   - if no regular file has that path, report `file_missing`;
   - if the file's hash or size differs from the manifest, report `file_modified`.
8. Every regular file whose path is not listed: `file_unlisted`.

The bundle procedure does not compare `bundle_id` with the directory's name; in ledger mode L3 compares it with
the entry.

### 8.5 What verification proves

An `OK` in ledger mode means:

- every entry from the genesis entry to the last line was signed by the key current at that point, starting from
  the trust anchor, and none was changed, removed from the middle, inserted or reordered;
- every bundle the ledger names is exactly the bundle that was sealed, and no other bundle directory exists.

It does not mean that the bundles' contents are true: that is the producer's responsibility, which the signature
makes attributable.

With head statements, an `OK` also means: for every statement, the ledger still holds the entry it names, unchanged.
It says nothing about entries after the newest statement (§11.1).

### 8.6 Head statements (step L5)

The procedure uses what L2 read:
- *N*, the number of entries that passed steps 1–5 (0 if L2 did not run);
- for each such entry *n*, its entry hash *H*(*n*) and the key *K*(*n*) that was current after it (step 11);
- the **known keys**: *A* if it is given, the genesis key L2 accepted, and every key a rotation made current.

For each head statement, in the order given; its findings carry the statement's position, and the procedure goes
on with the next statement after a finding:

1. **H1.** Parse it as a JSON object (§2) with a string `payload` and a string `sig`; an unpaired surrogate escape
   (§6.2) also fails this step. Otherwise report `head_malformed`.
2. **H2.** Parse the payload bytes as a JSON object (§2). `format` must be the string `"restore-evidence-head"` and
   `format_version` an integer; otherwise report `head_malformed`. If `format_version` is not `1`, report
   `format_unsupported`.
3. **H3.** Check the members of §6.8: `time` is a time, `key_id` and `entry_sha256` are hex64, and `seq` is an
   integer of at least 1. Otherwise report `head_malformed`.
4. **H4.** Let *n* = `seq`.
   - If *n* ≤ *N*: `key_id` must be key_id(*K*(*n*)) and `sig` must be the base64 of 64 bytes that is a valid
     signature by *K*(*n*) of the payload bytes; otherwise report `head_signature_invalid`. If it is, and
     `entry_sha256` ≠ *H*(*n*), report `head_mismatch`.
   - If *n* > *N*: if no known key has the key_id `key_id`, report `head_unverifiable`. Otherwise, if `sig` is not
     a valid signature by that key of the payload bytes (decoded as above), report `head_signature_invalid`;
     otherwise report `ledger_truncated`.

A statement that passes H1–H4 adds no finding.

## 9. Example

The first two lines of the vector `valid-ledger` (§10), and its trust anchor:

```
{"payload":"{\"format\":\"restore-evidence-ledger\",\"format_version\":1,\"seq\":1,\"time\":\"2026-11-16T12:00:00Z\",\"type\":\"key\",\"key_id\":\"59e1f9e806d8177ddb1e00536b129c25bb779040fe10a1d366954a99502b9574\",\"prev\":\"0000000000000000000000000000000000000000000000000000000000000000\",\"event\":\"genesis\",\"public_key\":\"FxLwO2LsDNaTzrAUZLAL50OG2sXWQ3+bcG5B6rmnUC0=\"}","sig":"HOlPYFKCyRNtmPO3kvFaPhEQcpc6+JvUFRyjfxutaOJHUrmiwQjjo4KKHsyuiI61oJeK0wvseUtZwX6Bri2FBg=="}
{"payload":"{\"format\":\"restore-evidence-ledger\",\"format_version\":1,\"seq\":2,\"time\":\"2026-11-16T12:00:00Z\",\"type\":\"bundle\",\"key_id\":\"59e1f9e806d8177ddb1e00536b129c25bb779040fe10a1d366954a99502b9574\",\"prev\":\"b8418dcf53fd0a9bbbcba4755fc91580937fa3047512ac7a5c6823ab5d37a9a7\",\"bundle_id\":\"b1\",\"bundle_sha256\":\"faefb14480919b0380586517f7d24a84503642bad6840723e48448cb3fbafd1e\",\"subject\":\"web1\",\"verdict\":\"OK\"}","sig":"yQW7V2rfJNitf9MBASMk0EtCWynFY/9+6k10oo4hYBlK1/o2QAaEwSx+h13ORfqkiVxL1QQvyzF/v2J4FAXODw=="}
```

```
anchor.pub: FxLwO2LsDNaTzrAUZLAL50OG2sXWQ3+bcG5B6rmnUC0=
```

How these two lines check out:

- **Line 1, key_id:** the key_id is the hash of the 32 bytes that `FxLw…UC0=` decodes to, and equals the anchor's.
- **Line 1, signature:** `sig` verifies against the payload's bytes, i.e. the text between the outer quotes once
  its `\"` escapes are decoded.
- **Line 2, chain:** its `prev` is the hash of line 1's payload bytes.
- **The bundle:** the hash of `bundles/b1/manifest.json`'s bytes is `faef…fd1e`.

## 10. Conformance vectors

`vectors/` holds one directory per vector:

| File | Meaning |
|---|---|
| `expected.json` | `description`, `mode` (`bundle` or `ledger`), `verdict`, and `findings`: the sorted set of finding codes |
| `anchor.pub` | the trust anchor (ledger mode) or the key (bundle mode), base64 of 32 bytes and a line feed; absent: none is given |
| `bundle/` | bundle mode: the bundle |
| `ledger.jsonl`, `bundles/` | ledger mode: the ledger file and the bundles directory |
| `heads/` | ledger mode, optional: head statements (§6.8), one per file, given to the verifier in the order of their file names |

- An implementation **conforms** when, for every vector, it returns exactly the declared verdict and exactly the
  declared set of codes.
- Vectors include valid ledgers and bundles, a key rotation, head statements, and at least one invalid case for each
  code of §8.3 except `v0_integrity`.
- The keys that signed the vectors are test keys derived from published seeds (Gaia's
  `internal/evidence/vectors_test.go`). They sign nothing else and MUST NOT be trusted for anything else.
- Some vectors contain empty files (`bundles/.keep`) only so that version control keeps an empty directory; such
  files are ignored by L4.
- Three vectors contain symbolic links (`bundle-file-type-invalid`, `bundle-manifest-symlink`, `ledger-bundle-symlink`).

## 11. Security considerations

### 11.1 Truncation

A ledger cut off after entry *k*, together with the bundles of the removed entries, verifies as `OK`: nothing
inside the data can tell that more existed. To detect it, the hash of the last entry (the **ledger head**) must be
recorded somewhere the producer cannot change. A head statement (§6.8) is that record: the producer signs it and
sends it elsewhere (Gaia puts it in its daily report, by email and webhook); a verifier given it (§8.6) reports
`ledger_truncated` for a ledger cut off before the entry it names, and `head_mismatch` for a ledger rewritten from
that entry on.

Limits:
- Entries written after the newest head statement the verifier holds can still be cut off undetected. The more
  often statements are sent, the shorter that window.
- The statement proves what the producer's key signed, not when: its `time` is the producer's clock (§11.3). An
  RFC 3161 timestamp of the statement would prove the time.
- A statement signed by a key that a truncated ledger no longer leads to cannot be checked (`head_unverifiable`,
  UNKNOWN): the ledger may have been cut off before a rotation. Keeping the trust anchor and the key_ids of later
  keys (e.g. from earlier reports) outside the ledger helps the verifier's operator judge such a case.

### 11.2 Keys

- The trust anchor decides everything; it must come from a channel the verified data cannot influence (§3).
- A rotation does not revoke: entries signed by a key before it was rotated stay valid. If a key is stolen, the
  thief can append entries signed by it until the ledger is rotated away from it, and can rotate the ledger to a
  key of their own. Only an external record of the ledger head (§11.1) shows that the history was extended or
  forked after a given point.

### 11.3 Other

- Times are what the producer's clock said. Use an external timestamp where time must be proven.
- `subject` and `verdict` are claims of the producer, signed so that they are attributable, not proven true.

## Appendix A — version 0 (Gaia, before this specification)

Gaia wrote bundles and ledgers in an earlier format (its evidence from 2026-11-15 and 2026-11-16, reports T7–T11,
is in this form). It is described here so that those bundles stay verifiable. **Nothing new is written in it.**
Implementations other than Gaia MAY treat version 0 as unsupported (`format_unsupported`); Gaia verifies it
read-only and reports problems with the code `v0_integrity` (CRITICAL).

**Recognition:**
- a manifest whose `format` is `"gaia-evidence/v1"`;
- a ledger whose first line is a JSON object with `"format": "gaia-ledger/v1"`.

(The `v1` in these old strings predates this specification and does not mean version 1.)

**Bundles:**
- The manifest has `format`, `bundle_id`, `created`, `subject`, `key_fingerprint` (= key_id) and `files` (as in §5.2).
- `manifest.sig` is the base64 of the signature of the manifest's bytes, with surrounding whitespace ignored.
- Everything that is not a regular file is an error.

**Ledger lines:**
- Each line is a JSON object with `format`, `seq`, `time`, `bundle_id`, `bundle_sha256`, `subject`, `verdict`,
  `prev`, `entry_sha256` and `sig`.
- `entry_sha256` is the hash of the line as Go's `encoding/json` marshals that object, with `entry_sha256` and
  `sig` set to empty and so omitted. This means: members in that order, `<`, `>` and `&` escaped as `<`,
  `>` and `&`, and `time` in RFC 3339 with nanoseconds as Go prints them.
- `sig` is the base64 of the signature of the **ASCII hex string** `entry_sha256`.
- `prev` chains as in §6.3.
- There are no key entries: one key, given by the verifier, signs everything.

This dependence on one library's serialisation is why version 1 signs the bytes it stores instead.
