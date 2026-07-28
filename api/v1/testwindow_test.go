package v1

import "time"

// INV-WINDOW-REQUIRED (DESIGN-v5 §1) makes both envelope bounds mandatory, so a
// fixture without a window is not a legal Budget and funds nothing. Most tests
// here are about something else entirely, so they take this deliberately WIDE
// window: open long before any test clock and closing long after, so it never
// silently becomes the thing under test. A test that IS about windows sets its
// own bounds instead.
var (
	testWindowStart = NewTime(time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC))
	testWindowEnd   = NewTime(time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC))
)
