package instancecontrol

// SetAdmissionFaultForTest exposes the existing syscall boundary only to the
// external-package engine integration tests. It is absent from product binaries.
func SetAdmissionFaultForTest(c *Control, fault func(string) error) {
	c.fault = fault
}

func SetAdmissionCheckpointForTest(c *Control, checkpoint func(string)) {
	c.checkpoint = checkpoint
}
