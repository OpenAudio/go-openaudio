package etl

import "testing"

func TestReadBackgroundJobsEnv(t *testing.T) {
	t.Run("defaults stay enabled", func(t *testing.T) {
		c := DefaultConfig()
		c.ReadBackgroundJobsEnv()
		if !c.EnableMaterializedViewRefresh || !c.EnableScheduledReleases || !c.EnablePgNotifyListener {
			t.Fatal("unset env vars must leave the background jobs enabled")
		}
	})

	t.Run("only false disables", func(t *testing.T) {
		for _, v := range []string{"true", "1", "yes", ""} {
			t.Setenv("OPENAUDIO_ETL_MV_REFRESH_ENABLED", v)
			c := DefaultConfig()
			c.ReadBackgroundJobsEnv()
			if !c.EnableMaterializedViewRefresh {
				t.Errorf("value %q should not disable the refresher", v)
			}
		}
	})

	t.Run("false disables each independently", func(t *testing.T) {
		t.Setenv("OPENAUDIO_ETL_MV_REFRESH_ENABLED", "false")
		c := DefaultConfig()
		c.ReadBackgroundJobsEnv()
		if c.EnableMaterializedViewRefresh {
			t.Error("mv refresh should be disabled")
		}
		if !c.EnableScheduledReleases || !c.EnablePgNotifyListener {
			t.Error("disabling one job must not affect the others")
		}
	})
}
