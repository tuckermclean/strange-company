package lab

// S3EmulatorArgs returns the arguments of `weed server` for the s3-emulator. It binds 127.0.0.1 only; VMs reach it
// through QEMU's host alias 10.0.2.2.
func S3EmulatorArgs(dataDir string) []string {
	return []string{"-s3", "-ip=127.0.0.1", "-ip.bind=127.0.0.1", "-dir=" + dataDir, "-s3.port=9000",
		"-master.volumeSizeLimitMB=64", "-volume.max=16"}
}
