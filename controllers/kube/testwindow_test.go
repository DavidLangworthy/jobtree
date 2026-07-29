package kube

import (
	"time"

	v1 "github.com/davidlangworthy/jobtree/api/v1"
)

// INV-WINDOW-REQUIRED (DESIGN-v5 §1) makes both envelope bounds mandatory, so a
// fixture without a window is not a legal Budget and funds nothing. Most tests
// here are about something else entirely, so they take this deliberately WIDE
// window: open long before any test clock and closing long after, so it never
// silently becomes the thing under test. A test that IS about windows sets its
// own bounds instead.
var (
	testWindowStart = v1.NewTime(time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC))
	testWindowEnd   = v1.NewTime(time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC))
)
