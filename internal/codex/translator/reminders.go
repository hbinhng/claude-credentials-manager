package translator

import "regexp"

// droppedReminderPatterns matches `<system-reminder>` blocks whose
// content is known to drive output loops on non-Anthropic models. The
// "task tools haven't been used recently" reminder is the canonical
// case from the comprehensive-fix spec: it fires AFTER TaskUpdate
// just succeeded, contradicts the assistant's prior turn, and the
// model retries forever.
//
// Patterns are anchored on the literal `<system-reminder>` tags so
// bare prose mentioning the same phrase is not matched.
var droppedReminderPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?s)<system-reminder>\s*The task tools haven't been used recently\..*?</system-reminder>`),
}

// stripDroppedReminders removes every span in s matched by any pattern
// in droppedReminderPatterns. Returns the surviving text. Callers are
// responsible for dropping the resulting block entirely if it becomes
// empty / whitespace-only.
func stripDroppedReminders(s string) string {
	for _, re := range droppedReminderPatterns {
		s = re.ReplaceAllString(s, "")
	}
	return s
}
