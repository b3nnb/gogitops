# Homebrew formula for recipe-lint
#
# This is a formula template for the gogitops recipe-lint tool.
# To use as a Homebrew tap:
#
#   1. Create a tap repo: github.com/b3nnb/homebrew-tap
#   2. Copy this file to Formula/recipe-lint.rb in the tap repo
#   3. Install: brew install b3nnb/tap/recipe-lint
#
# For the template, the version and checksum are placeholders.
# In the real tap, these are updated on each release via:
#   brew bump-formula-pr --url=<tarball-url> --sha256=<sha> recipe-lint

class RecipeLint < Formula
  desc "Static validator/linter for gogitops recipe YAML files"
  homepage "https://github.com/b3nnb/gogitops"
  url "https://github.com/b3nnb/gogitops/archive/refs/tags/v1.0.0.tar.gz"
  # sha256: replace with actual checksum after tagging a release
  sha256 "0000000000000000000000000000000000000000000000000000000000000000"
  version "1.0.0"
  license "MIT"

  depends_on "go" => :build

  def install
    system "go", "build", *std_go_args(ldflags: "-X main.Version=#{version}"),
           "./cmd/recipe-lint"
  end

  test do
    assert_match "recipe-lint", shell_output("#{bin}/recipe-lint --version")
  end
end