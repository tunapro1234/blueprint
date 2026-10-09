Each `*.txt` file is one attack text. The first line is
`# expect: <kind>[,<kind>...]`; the rest is the body. `TestAttackCorpus` frames
the body and fails if a listed kind is not flagged or a body line escapes the
frame. Every attack bp-redteam confirms gets a file here.
