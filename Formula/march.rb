# march itself.
#
# Installing this brings the whole stack with it: the TUI, and the patched QEMU
# that gives guests hardware-accelerated graphics. march detects that QEMU
# automatically, so a plain `brew install melvinsh/march/march` is all that is
# needed to get an accelerated Arch desktop.
class March < Formula
  desc "Install and manage hardware-accelerated Arch Linux ARM VMs on QEMU"
  homepage "https://github.com/melvinsh/march"
  url "https://github.com/melvinsh/march/archive/refs/tags/v1.6.1.tar.gz"
  sha256 "0b7e2e47dcc5b7dba6c3477acd7bfa14d8509dcb127a55773d4a090b187b5676"
  license "MIT"
  version "1.6.1"

  depends_on "go" => :build

  # The accelerated QEMU. Without it march still works, falling back to whatever
  # qemu is on PATH, but guests render on the CPU instead of the GPU.
  depends_on "melvinsh/march/qemu-march"

  # The GPU stack qemu-march links against. Listed so `brew install march`
  # pulls everything in one step.
  depends_on "startergo/angle/angle"
  depends_on "startergo/libepoxy/libepoxy"
  depends_on "startergo/virglrenderer/virglrenderer"

  def install
    ldflags = "-s -w -X main.version=#{version}"
    system "go", "build", *std_go_args(ldflags: ldflags)
  end

  def caveats
    <<~EOS
      march stores VMs and installer images under:
        #{Dir.home}/.local/share/march

      Guests render on the host GPU through #{Formula["melvinsh/march/qemu-march"].opt_bin}.
      march finds it automatically; nothing needs to be added to PATH.

      To let a VM receive every keystroke — including Cmd+Space, which the
      desktop binds to its launcher — grant Accessibility permission to the
      terminal you run march from:
        System Settings -> Privacy & Security -> Accessibility
    EOS
  end

  test do
    assert_match "march", shell_output("#{bin}/march -version")

    # The accelerated QEMU must be present and able to do both of the things
    # march needs from it: hardware GL and user-mode networking.
    qemu = Formula["melvinsh/march/qemu-march"].opt_bin/"qemu-system-aarch64"
    assert_match "virtio-gpu-gl-pci", shell_output("#{qemu} -device help 2>&1")

    # QMP over stdio would wait forever without a quit command, so the check
    # feeds one and lets QEMU exit on its own.
    net = pipe_output(
      "#{qemu} -M virt -netdev user,id=n0 -nodefaults -display none " \
      "-S -monitor none -qmp stdio 2>&1",
      %q({"execute":"qmp_capabilities"}{"execute":"quit"}),
    )
    refute_match "is not compiled into this binary", net
  end
end
