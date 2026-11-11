package freedom

import (
	"testing"

	"github.com/roseforljh/RayBox-rx/internal/protocol/common/net"
	"github.com/roseforljh/RayBox-rx/internal/protocol/common/session"
)

func TestMasqueDefaultFinalRule(t *testing.T) {
	h := &Handler{}
	rule := getDefaultFinalRule(&session.Inbound{Name: "masque"})
	if rule != defaultBlockPrivateRule {
		t.Fatal("MASQUE has no default private-address protection")
	}
	private := net.ParseAddress("127.0.0.1")
	if got := h.matchFinalRule(net.Network_TCP, private, 443, rule); got != rule {
		t.Fatal("private target not blocked")
	}
	if got := h.matchFinalRule(net.Network_UDP, net.ParseAddress("1.1.1.1"), 443, rule); got != nil {
		t.Fatal("public target blocked")
	}
	allow, err := buildFinalRule(&FinalRuleConfig{Action: RuleAction_Allow})
	if err != nil {
		t.Fatal(err)
	}
	h.finalRules = []*FinalRule{allow}
	if got := h.matchFinalRule(net.Network_TCP, private, 443, rule); got != allow {
		t.Fatal("explicit allow must override default rule")
	}
}
