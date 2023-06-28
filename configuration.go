package main

type Configuration struct {
	ActiveDirectory struct {
		Enabled  bool
		Host     string
		Domain   string
		Username string
		Password string
		Dn       string
	}
	Azure struct {
		Enabled bool
		Domain  string
	}
	Logging struct {
		Enabled  bool
		Location string
	}
	Database struct {
		Host            string
		Port            int
		Name            string
		Trusted         bool
		Domain          string
		Username        string
		Password        string
		ExemptComputers []string
	}
}
