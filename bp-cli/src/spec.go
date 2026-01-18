package bp

func (b *Blueprint) SpecHash() (string, error) {
	section, err := b.GetSection("implementation")
	if err != nil {
		return "", err
	}
	if section == nil {
		section = map[string]interface{}{}
	}
	canonical := canonicalizeValue(section)
	return HashString(canonical), nil
}
