package commercial_test

import (
	"os/exec"
	"testing"
)

func TestBrowserArithmeticGoldenVectors(t *testing.T) {
	out, err := exec.Command("node", "--test", "testdata/arithmetic.cjs").CombinedOutput()
	t.Logf("%s", out)
	if err != nil {
		t.Fatal(err)
	}
}
