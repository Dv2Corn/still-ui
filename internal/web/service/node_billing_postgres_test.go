package service

import (
	"os"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/testpg"
)

// The per-client billing predicates are built SQL; PostgreSQL must run them as SQLite does.
func TestNodeBillingPostgres(t *testing.T) {
	if os.Getenv("XUI_DB_TYPE") != "postgres" || os.Getenv("XUI_DB_DSN") == "" {
		t.Skip("set XUI_DB_TYPE=postgres and XUI_DB_DSN")
	}
	for _, tc := range []struct {
		name string
		run  func(*testing.T)
	}{
		{"billed quota after a push", TestNodeBillingFallbackCutsAtBilledQuota},
		{"push is the master's count", TestNodeBillingFreshPushIsTheMastersCount},
		{"per-client master", TestNodeBillingPerClientMaster},
		{"multiplier 1 unchanged", TestNodeBillingOneIsByteIdentical},
		{"reset re-enables", TestNodeBillingResetReenables},
		{"renewal re-enables", TestNodeBillingRenewalReenables},
		{"periodic reset", TestNodeBillingPeriodicResetSeesBilledUsage},
		{"master ignores re-billed history", TestMasterIgnoresANodeCutThatRebillsHistory},
		{"change midway", TestNodeBillingChangeMidwayDoesNotRebill},
		{"base reset and old rows", TestNodeBillingBaseResetAndOldRows},
		{"two tiers for one client", TestNodeBillingTwoTiersForOneClient},
		{"SQL and Go agree", TestNodeBillingSQLAndGoAgree},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cleanup, err := testpg.IsolatePackage("node_billing")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(cleanup)
			tc.run(t)
		})
	}
}
