# puffin, as a homebrew formula.
#
# This file is the SOURCE for the formula; the tap holds a copy.
# `VERSION=vX.Y.Z game build formula` renders it with the tag and revision
# filled in, which is the step that keeps the tap from drifting away from
# the release.
#
# A formula rather than a cask, because a cask is for a pre-built app bundle
# or pkg and this is a Go binary built from source.
#
# It builds from the git URL at a tag rather than from a release asset.
# Both work; the git URL needs nothing set up by the person installing it,
# while the asset URL brings api.github.com and a token with it. The release
# asset still exists and is still the right thing for a direct download; it
# is just not what brew reads.
class Puffin < Formula
  desc "TUI for the enclave: every service that publishes an api"
  homepage "https://github.com/janearc/puffin-dist"
  url "https://github.com/janearc/puffin-dist.git", tag: "VERSION_PLACEHOLDER", revision: "REVISION_PLACEHOLDER"
  version "VERSION_PLACEHOLDER"
  head "https://github.com/janearc/puffin-dist.git", branch: "main"

  depends_on "go" => :build
  # kubectl is not optional: fourteen call sites, and every cluster screen is
  # dark without it.
  depends_on "kubernetes-cli"

  # NOT declared, deliberately: tmux, colima, k3d, docker, terminal-notifier.
  # Each is needed by one screen and none is needed to run. puffin already
  # says which one is missing when a pane cannot do its job, and a formula
  # that drags in colima to read a log is a formula nobody installs.

  def install
    # the version is stamped from the tag, so `puffin --age` and the formula
    # cannot disagree. The subject is the tag too: a brew build has the git
    # history available, but reading it here would report the commit's
    # message rather than the release's identity, and the release is what
    # somebody installed.
    ldflags = %W[
      -s -w
      -X main.buildVersion=#{version}
      -X main.buildSubject=released\ as\ #{version}
    ]
    system "go", "build", *std_go_args(ldflags: ldflags.join(" ")), "./cmd/puffin"
  end

  test do
    # --age is the one command that needs nothing but the binary: no cluster,
    # no kubeconfig, no terminal. It prints what this build is, which is
    # exactly what a formula test should check.
    assert_match "puffin", shell_output("#{bin}/puffin --age")
  end
end
