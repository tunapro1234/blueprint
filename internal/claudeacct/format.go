package claudeacct

import (
	"fmt"
	"io"
	"math"
	"strings"
	"time"
)

// FormatDuration renders a short human duration: 45s, 4m, 3h12m, 2d5h.
// It rounds to the second first so a wait computed a moment after it was set
// (Retry-After 240 seen 1ms later) still reads 4m, not 3m.
func FormatDuration(d time.Duration) string {
	d = d.Round(time.Second)
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		h, m := int(d.Hours()), int(d.Minutes())%60
		if m == 0 {
			return fmt.Sprintf("%dh", h)
		}
		return fmt.Sprintf("%dh%dm", h, m)
	default:
		days, h := int(d.Hours())/24, int(d.Hours())%24
		if h == 0 {
			return fmt.Sprintf("%dd", days)
		}
		return fmt.Sprintf("%dd%dh", days, h)
	}
}

// liveTokenStatus describes the live login. bp never refreshes it.
func liveTokenStatus(t Tokens, now time.Time) string {
	switch {
	case t.AccessToken == "":
		return "missing"
	case t.ExpiresAt <= 0:
		return "valid"
	case !now.Before(t.Expiry()):
		return "token expired"
	default:
		return "valid, expires in " + FormatDuration(t.Expiry().Sub(now))
	}
}

// storedTokenStatus describes an inactive slot's stored login.
func storedTokenStatus(t Tokens, slot Slot, now time.Time) string {
	switch {
	case slot.Dead:
		return "refresh dead"
	case t.RefreshToken == "":
		return "no refresh token"
	case t.AccessToken == "" || t.ExpiresAt > 0 && !now.Before(t.Expiry()):
		return "expired"
	case t.ExpiresAt <= 0:
		return "valid"
	default:
		return "valid, expires in " + FormatDuration(t.Expiry().Sub(now))
	}
}

func formatWindow(label string, w *Window, now time.Time) string {
	if w == nil {
		return label + " n/a"
	}
	text := fmt.Sprintf("%s %d%%", label, int(math.Round(w.effective(now))))
	if !w.ResetsAt.IsZero() && w.ResetsAt.After(now) {
		text += " (resets " + FormatDuration(w.ResetsAt.Sub(now)) + ")"
	}
	return text
}

// UsageLine is the usage part of a listed slot.
func UsageLine(cache *UsageCache, now time.Time) string {
	var parts []string
	if cache == nil || cache.Usage == nil {
		parts = append(parts, "5h n/a  7d n/a")
	} else {
		parts = append(parts, formatWindow("5h", cache.Usage.FiveHour, now)+"  "+formatWindow("7d", cache.Usage.SevenDay, now))
		parts = append(parts, "updated "+FormatDuration(now.Sub(cache.FetchedAt))+" ago")
	}
	if cache != nil && cache.Error != "" {
		text := "usage: error (" + cache.Error
		if cache.BackoffUntil.After(now) {
			text += ", retry " + FormatDuration(cache.BackoffUntil.Sub(now))
		}
		parts = append(parts, text+")")
	}
	return strings.Join(parts, "  ")
}

func slotLabel(s Slot) string {
	label := fmt.Sprintf("slot %d", s.Number)
	if s.Alias != "" {
		label += " [" + s.Alias + "]"
	}
	return label
}

// RenderList prints the slot table of bp account list.
func RenderList(w io.Writer, o *Overview) {
	if len(o.Slots) == 0 {
		fmt.Fprintln(w, "no stored Claude accounts; log in with claude, then run bp account add")
	}
	for _, s := range o.Slots {
		marker := " "
		if s.Active {
			marker = "*"
		}
		who := s.Email
		if s.OrgName != "" {
			who += " (" + s.OrgName + ")"
		}
		var flags []string
		if s.Subscription != "" {
			flags = append(flags, s.Subscription)
		}
		if s.Disabled {
			flags = append(flags, "disabled")
		}
		if s.Dead {
			flags = append(flags, "dead")
		}
		line := fmt.Sprintf("%s %s  %s", marker, slotLabel(s.Slot), who)
		if len(flags) > 0 {
			line += "  " + strings.Join(flags, ", ")
		}
		fmt.Fprintln(w, line)
		fmt.Fprintf(w, "    %s  token: %s\n", UsageLine(s.LastUsage, o.Now), s.Token)
	}
	if o.LiveSlot == 0 {
		fmt.Fprintln(w, liveSummary(o))
	}
}

func liveSummary(o *Overview) string {
	switch {
	case o.Live != nil:
		return fmt.Sprintf("live login %s is not stored; run bp account add to keep it", o.Live.Email)
	case o.LiveToken == "not logged in":
		return "no live Claude Code login in " + o.ConfigHome
	default:
		return "the live Claude Code login has no account identity in " + o.GlobalConfig
	}
}

// RenderStatus prints bp account status.
func RenderStatus(w io.Writer, o *Overview, auto AutoState) {
	var active *SlotView
	for i := range o.Slots {
		if o.Slots[i].Active {
			active = &o.Slots[i]
		}
	}
	if active == nil {
		fmt.Fprintln(w, liveSummary(o))
	} else {
		fmt.Fprintf(w, "active: %s  %s  token: %s\n", slotLabel(active.Slot), active.Email, active.Token)
		fmt.Fprintf(w, "usage: %s\n", UsageLine(active.LastUsage, o.Now))
	}
	usable := 0
	for _, s := range o.Slots {
		if s.Usable() {
			usable++
		}
	}
	fmt.Fprintf(w, "stored: %d accounts, %d usable\n", len(o.Slots), usable)
	if !auto.LastSwitchAt.IsZero() {
		fmt.Fprintf(w, "last switch: slot %d -> slot %d, %s ago\n", auto.LastFrom, auto.LastTo, FormatDuration(o.Now.Sub(auto.LastSwitchAt)))
	}
	fmt.Fprintf(w, "claude config: %s\n", o.ConfigHome)
}
