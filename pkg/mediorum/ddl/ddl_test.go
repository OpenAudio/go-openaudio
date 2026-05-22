package ddl

import "testing"

func TestRunVacuumFullDefaultOffDoesNotTouchDatabase(t *testing.T) {
	t.Setenv("OPENAUDIO_MEDIORUM_VACUUM_FULL", "")

	runVacuumFull(nil)
}

func TestVacuumFullEnabledDefaultsOff(t *testing.T) {
	t.Setenv("OPENAUDIO_MEDIORUM_VACUUM_FULL", "")

	if vacuumFullEnabled() {
		t.Fatal("expected vacuum full to default off")
	}
}

func TestVacuumFullEnabledParsesBool(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  bool
	}{
		{name: "true", value: "true", want: true},
		{name: "one", value: "1", want: true},
		{name: "false", value: "false", want: false},
		{name: "zero", value: "0", want: false},
		{name: "invalid", value: "yes", want: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("OPENAUDIO_MEDIORUM_VACUUM_FULL", test.value)

			if got := vacuumFullEnabled(); got != test.want {
				t.Fatalf("vacuumFullEnabled() = %v, want %v", got, test.want)
			}
		})
	}
}
