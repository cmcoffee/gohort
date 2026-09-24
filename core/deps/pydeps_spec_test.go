package deps

import "testing"

// Only a plain PyPI requirement may reach pip. Every refused shape is a way
// to run code or read files on the host at install time.
func TestValidatePySpecRefusesAnythingButARequirement(t *testing.T) {
	for _, ok := range []string{
		"openpyxl", "python-docx", "python_pptx", "pandas>=2.0",
		"pandas>=2.0,<3", "requests[socks]==2.31.0", "zope.interface",
	} {
		if err := ValidatePySpec(ok); err != nil {
			t.Errorf("%q should be allowed: %v", ok, err)
		}
	}
	for _, bad := range []string{
		"-e .", "--index-url=http://x", "--target=/tmp", "pkg @ https://x/pkg.tar.gz",
		"https://x/pkg.whl", "./local", "/abs/path", "git+https://x/r.git",
		"pkg; os_name=='posix'", "pkg ==1.0", "",
	} {
		if err := ValidatePySpec(bad); err == nil {
			t.Errorf("%q should be refused", bad)
		}
	}
}

func TestPySpecNameNormalizes(t *testing.T) {
	cases := map[string]string{
		"python_docx":        "python-docx",
		"Python.Docx==1.1":   "python-docx",
		"requests[socks]>=2": "requests",
		"openpyxl":           "openpyxl",
	}
	for in, want := range cases {
		if got := PySpecName(in); got != want {
			t.Errorf("PySpecName(%q) = %q, want %q", in, got, want)
		}
	}
}
