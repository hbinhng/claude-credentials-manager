package translator

import "testing"

func TestStripDroppedReminders_RemovesTaskToolsReminder(t *testing.T) {
	in := "Some real text.\n<system-reminder>\nThe task tools haven't been used recently. blah blah\nlonger text\n</system-reminder>\nMore real text."
	got := stripDroppedReminders(in)
	if got == in {
		t.Fatalf("stripDroppedReminders did not remove the reminder block")
	}
	want := "Some real text.\n\nMore real text."
	if got != want {
		t.Errorf("stripDroppedReminders = %q, want %q", got, want)
	}
}

func TestStripDroppedReminders_AllReminderInputBecomesEmpty(t *testing.T) {
	in := "<system-reminder>\nThe task tools haven't been used recently. xyz\n</system-reminder>"
	got := stripDroppedReminders(in)
	if got != "" {
		t.Errorf("stripDroppedReminders all-reminder input = %q, want empty", got)
	}
}

func TestStripDroppedReminders_PreservesUnrelatedReminder(t *testing.T) {
	in := "<system-reminder>\nUser denied this tool call.\n</system-reminder>"
	got := stripDroppedReminders(in)
	if got != in {
		t.Errorf("stripDroppedReminders should preserve unrelated reminder, got %q", got)
	}
}

func TestStripDroppedReminders_PreservesProseMentioningPhrase(t *testing.T) {
	// Bare prose without <system-reminder> tags must not be matched.
	in := "I noticed the task tools haven't been used recently in this session."
	got := stripDroppedReminders(in)
	if got != in {
		t.Errorf("stripDroppedReminders should preserve bare prose, got %q", got)
	}
}
