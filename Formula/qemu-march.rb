# QEMU built the way march needs it: hardware-accelerated guest graphics *and*
# user-mode networking, in one binary that does not displace the stock qemu.
#
# Neither of the two builds you can otherwise get works for march:
#
#   * Homebrew's qemu has networking but no OpenGL at all, so guests render on
#     the CPU through llvmpipe.
#   * The virgl taps have OpenGL but omit libslirp, so guests have no network —
#     which march's installer depends on entirely. They also take the name
#     "qemu", so installing one uninstalls the other.
#
# This formula is keg_only and named qemu-march, so it sits alongside the stock
# qemu rather than replacing it. march prefers it when present and falls back
# to the stock binary otherwise.
class QemuMarch < Formula
  desc "QEMU with virgl GPU acceleration and user-mode networking, for march"
  homepage "https://www.qemu.org/"
  url "https://download.qemu.org/qemu-11.0.3.tar.xz"
  sha256 "da5fcffc32762820568b828ed430a728864d34d50b6d2f30358597760cbb0523"
  license "GPL-2.0-only"

  # The GPU stack. These come from the taps that already package ANGLE and a
  # macOS-capable virglrenderer; march does not rebuild them.
  depends_on "startergo/angle/angle"
  depends_on "startergo/libepoxy/libepoxy"
  depends_on "startergo/virglrenderer/virglrenderer"
  depends_on "molten-vk"

  # libslirp is the piece the virgl taps leave out, and the reason guests there
  # have no network.
  depends_on "libslirp"

  depends_on "glib"
  depends_on "gnutls"
  depends_on "jpeg-turbo"
  depends_on "libpng"
  depends_on "libssh"
  depends_on "libusb"
  depends_on "lzo"
  depends_on "ncurses"
  depends_on "nettle"
  depends_on "pixman"
  depends_on "snappy"
  depends_on "zstd"

  depends_on "meson" => :build
  depends_on "ninja" => :build
  depends_on "pkgconf" => :build
  depends_on "python@3.13" => :build

  # Keeping it keg_only is the whole point: the stock qemu stays linked and
  # working, and march reaches into this keg by path.
  keg_only "it is a patched QEMU that must not displace the stock one"

  # Teaches QEMU's Cocoa display to render through OpenGL on macOS, which is
  # what makes virtio-gpu-gl usable. Derived from Akihiko Odaki's work, rebased
  # onto this QEMU release; see Formula/patches/NOTICE.
  patch do
    url "file://#{__dir__}/patches/qemu-11.0.3-macos-gl.patch"
    sha256 "b5b01a2c84f18e9941c980c7113106789d4247cfa4adef48395d270908fa8275"
  end

  def install
    # march only ever runs aarch64 guests. Building the one target instead of
    # every architecture turns a very long build into a short one.
    args = %W[
      --prefix=#{prefix}
      --target-list=aarch64-softmmu
      --enable-cocoa
      --enable-opengl
      --enable-virglrenderer
      --enable-slirp
      --enable-hvf
      --disable-sdl
      --disable-gtk
      --disable-vnc
      --disable-docs
      --disable-guest-agent
      --disable-werror
      --extra-cflags=-I#{Formula["startergo/angle/angle"].opt_include}
      --extra-ldflags=-L#{Formula["startergo/angle/angle"].opt_lib}
    ]

    system "./configure", *args
    system "make", "-j#{ENV.make_jobs}"
    system "make", "install"
  end

  test do
    # The binary must exist, speak aarch64, and carry both of the things this
    # formula exists to combine.
    out = shell_output("#{bin}/qemu-system-aarch64 --version")
    assert_match "QEMU emulator version", out

    devices = shell_output("#{bin}/qemu-system-aarch64 -device help 2>&1")
    assert_match "virtio-gpu-gl-pci", devices

    # A build without libslirp reports the backend as not compiled in. QMP over
    # stdio would wait forever without a quit command, so one is fed in.
    net = pipe_output(
      "#{bin}/qemu-system-aarch64 -M virt -netdev user,id=n0 -nodefaults " \
      "-display none -S -monitor none -qmp stdio 2>&1",
      %q({"execute":"qmp_capabilities"}{"execute":"quit"}),
    )
    refute_match "is not compiled into this binary", net

    # The whole point of this formula: GL and networking in one binary.
    assert_match "virtio-gpu-gl-pci", shell_output("#{bin}/qemu-system-aarch64 -device help 2>&1")
  end
end
