package host

import (
	"context"
	"testing"
)

// A live report of what this machine can actually do, so a Venus regression
// shows up as a readable line rather than a failed boot.
func TestVenusOnThisHost(t *testing.T) {
	c, err := Probe(context.Background())
	if err != nil {
		t.Skipf("no QEMU: %v", err)
	}
	t.Logf("gl=%v venusDevice=%v moltenvk=%q loader=%q seqpacket=%v => SupportsVenus=%v",
		c.SupportsGPUAccel(), c.VenusDevice, c.MoltenVK, c.VulkanLoader,
		c.SeqPacket, c.SupportsVenus())

	// Venus is served only through virglrenderer's render-server proxy, whose
	// transport needs SOCK_SEQPACKET. Claiming Venus without it produces a
	// QEMU that exits during firmware init and a guest that never boots.
	if c.SupportsVenus() && !c.SeqPacket {
		t.Error("Venus was claimed on a host that cannot create a SOCK_SEQPACKET pair")
	}
}
