Review the note another agent wrote for fictional record {{.Key}}.

Note: {{.Steps.code.Output}} (written by {{.Steps.code.Agent}}, result {{.Steps.code.Result}})
Company: {{.Unit.company}}
Category: {{.Unit.category}}

Write one JSON object to {{.Output}} with keys slug ("{{.Key}}"), verdict
("accept" or "revise") and comments (a list of short strings). Do not edit the
note itself.
