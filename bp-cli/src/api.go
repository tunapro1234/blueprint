package bp

func (b *Blueprint) APIHash() (string, error) {
	section, err := b.GetSection("api")
	if err != nil {
		return "", err
	}
	if section == nil {
		section = map[string]interface{}{}
	}
	canonical := canonicalizeValue(section)
	return HashString(canonical), nil
}
