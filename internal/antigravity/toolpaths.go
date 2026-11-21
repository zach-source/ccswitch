package antigravity

// SetToolPaths points the security and agy commands at other programs and
// returns a function that restores the old paths. Tests in other packages use
// it to run against fake scripts, never the login keychain.
func SetToolPaths(security, agy string) (restore func()) {
	oldSec, oldAgy := securityBin, agyBin
	securityBin, agyBin = security, agy
	return func() { securityBin, agyBin = oldSec, oldAgy }
}
