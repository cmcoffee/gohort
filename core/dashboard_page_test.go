package core

import (
	"strings"
	"testing"
)

// A notice offers Dismiss only when it names where to post it, and the
// address is escaped like the rest.
func TestANoticeOffersDismissWhenItCan(t *testing.T) {
	plain := dashboardNotices([]DashboardNotice{{Text: "Something.", Action: "Open", URL: "/x"}})
	if strings.Contains(plain, "data-dismiss") {
		t.Errorf("a notice with no dismiss address offers Dismiss: %s", plain)
	}
	with := dashboardNotices([]DashboardNotice{{Text: "Lead <failing>.", Action: "Open", URL: "/x", Dismiss: `/admin/api/lead-llm/dismiss?a="b"`}})
	if !strings.Contains(with, `data-dismiss="/admin/api/lead-llm/dismiss?a=&#34;b&#34;"`) || !strings.Contains(with, "Lead &lt;failing&gt;.") {
		t.Errorf("dismiss: %s", with)
	}
}
