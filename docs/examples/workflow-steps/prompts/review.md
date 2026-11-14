Review the note another agent wrote for fictional record {{.Key}}.

Note: {{.Steps.code.Output}} (written by {{.Steps.code.Agent}}, result {{.Steps.code.Result}})
Company: {{.Unit.company}}
Category: {{.Unit.category}}

Write one JSON object to {{.Output}} with keys slug ("{{.Key}}"), verdict
("accept" or "revise") and comments (a list of short strings). Do not edit the
note itself.
{{- if gt .Attempt 1}}

This is review attempt {{.Attempt}}; the previous attempt ended
{{.Previous.Result}}. Your earlier review is still at {{.Output}}: check that
the note now addresses its comments, then overwrite it.
{{- end}}
