// Copyright 2023 The Cockroach Authors.
//
// Use of this software is governed by the Business Source License
// included in the file licenses/BSL.txt.
//
// As of the Change Date specified in that file, in accordance with
// the Business Source License, use of this software will be governed
// by the Apache License, Version 2.0, included in the file
// licenses/APL.txt.

package sql

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/cockroachdb/cockroach/pkg/base"
	"github.com/cockroachdb/cockroach/pkg/jobs"
	"github.com/cockroachdb/cockroach/pkg/server/serverpb"
	"github.com/cockroachdb/cockroach/pkg/settings"
	"github.com/cockroachdb/cockroach/pkg/settings/cluster"
	"github.com/cockroachdb/cockroach/pkg/sql/sqlstats"
	"github.com/cockroachdb/cockroach/pkg/sql/sqlstats/persistedsqlstats"
	"github.com/cockroachdb/cockroach/pkg/testutils"
	"github.com/cockroachdb/cockroach/pkg/testutils/serverutils"
	"github.com/cockroachdb/cockroach/pkg/testutils/sqlutils"
	"github.com/cockroachdb/cockroach/pkg/upgrade/upgradebase"
	"github.com/cockroachdb/cockroach/pkg/util/leaktest"
	"github.com/cockroachdb/cockroach/pkg/util/log"
	"github.com/cockroachdb/cockroach/pkg/util/protoutil"
	"github.com/cockroachdb/cockroach/pkg/util/timeutil"
	"github.com/cockroachdb/cockroach/pkg/util/uuid"
	"github.com/stretchr/testify/require"
)

// TestSqlActivityUpdateJob verifies that the
// job is created.
func TestSqlActivityUpdateJob(t *testing.T) {
	defer leaktest.AfterTest(t)()
	defer log.Scope(t).Close(t)

	ctx := context.Background()

	stubTime := timeutil.Now().Truncate(time.Hour)
	sqlStatsKnobs := sqlstats.CreateTestingKnobs()
	sqlStatsKnobs.StubTimeNow = func() time.Time { return stubTime }

	// Start the cluster.
	// Disable the job since it is called manually from a new instance to avoid
	// any race conditions.
	srv, sqlDB, _ := serverutils.StartServer(t, base.TestServerArgs{
		Insecure: true,
		Knobs: base.TestingKnobs{
			SQLStatsKnobs: sqlStatsKnobs,
			UpgradeManager: &upgradebase.TestingKnobs{
				DontUseJobs:                       true,
				SkipUpdateSQLActivityJobBootstrap: true,
			}}})
	defer srv.Stopper().Stop(context.Background())
	defer sqlDB.Close()

	db := sqlutils.MakeSQLRunner(sqlDB)
	db.Exec(t, "SET database = crdb_internal.current_observability_database()")

	var count int
	row := db.QueryRow(t, "SELECT count_rows() FROM transaction_activity")
	row.Scan(&count)
	require.Equal(t, 0, count, "transaction_activity: expect:0, actual:%d", count)

	row = db.QueryRow(t, "SELECT count_rows() FROM statement_activity")
	row.Scan(&count)
	require.Equal(t, 0, count, "statement_activity: expect:0, actual:%d", count)

	row = db.QueryRow(t,
		"SELECT count_rows() FROM system.public.jobs WHERE job_type = 'AUTO UPDATE SQL ACTIVITY' and id = 103")
	row.Scan(&count)
	require.Equal(t, 0, count, "jobs: expect:0, actual:%d", count)

	row = db.QueryRow(t, "SELECT count_rows() FROM transaction_statistics")
	row.Scan(&count)
	require.Equal(t, 0, count, "transaction_statistics: expect:0, actual:%d", count)

	row = db.QueryRow(t, "SELECT count_rows() FROM statement_statistics")
	row.Scan(&count)
	require.Equal(t, 0, count, "statement_statistics: expect:0, actual:%d", count)

	row = db.QueryRow(t, "SELECT count_rows() FROM crdb_internal.transaction_activity")
	row.Scan(&count)
	require.Equal(t, 0, count, "transaction_activity: expect:0, actual:%d", count)

	row = db.QueryRow(t, "SELECT count_rows() FROM crdb_internal.statement_activity")
	row.Scan(&count)
	require.Equal(t, 0, count, "statement_activity: expect:0, actual:%d", count)

	execCfg := srv.ExecutorConfig().(ExecutorConfig)
	st := cluster.MakeTestingClusterSettings()
	updater := newSqlActivityUpdater(st, execCfg.InternalDB, sqlStatsKnobs)

	require.NoError(t, updater.TransferStatsToActivity(ctx))

	row = db.QueryRow(t, "SELECT count_rows() FROM transaction_activity")
	row.Scan(&count)
	require.Equal(t, 0, count, "transaction_activity: expect:0, actual:%d", count)

	row = db.QueryRow(t, "SELECT count_rows() FROM statement_activity")
	row.Scan(&count)
	require.Equal(t, 0, count, "statement_activity: expect:0, actual:%d", count)

	appName := "TestSqlActivityUpdateJob"
	db.Exec(t, "SET SESSION application_name=$1", appName)
	db.Exec(t, "SELECT 1;")

	srv.SQLServer().(*Server).GetSQLStatsProvider().(*persistedsqlstats.PersistedSQLStats).Flush(ctx)

	db.Exec(t, "SET SESSION application_name=$1", "randomIgnore")

	// Run the updater to add rows to the activity tables.
	// This will use the transfer all scenarios with there only
	// being a few rows.
	require.NoError(t, updater.TransferStatsToActivity(ctx))

	row = db.QueryRow(t, "SELECT count_rows() FROM transaction_activity WHERE app_name = $1", appName)
	row.Scan(&count)
	require.Equal(t, count, 1, "transaction_activity after transfer: expect:1, actual:%d", count)

	row = db.QueryRow(t, "SELECT count_rows() FROM statement_activity WHERE app_name = $1", appName)
	row.Scan(&count)
	require.Equal(t, count, 1, "statement_activity after transfer: expect:1, actual:%d", count)

	row = db.QueryRow(t, "SELECT count_rows() FROM crdb_internal.transaction_activity WHERE app_name = $1", appName)
	row.Scan(&count)
	require.Equal(t, count, 1, "crdb_internal.transaction_activity after transfer: expect:1, actual:%d", count)

	row = db.QueryRow(t, "SELECT count_rows() FROM crdb_internal.statement_activity WHERE app_name = $1", appName)
	row.Scan(&count)
	require.Equal(t, count, 1, "crdb_internal.statement_activity after transfer: expect:1, actual:%d", count)

	// Reset the stats and verify all sql stats tables are empty.
	db.Exec(t, "SELECT crdb_internal.reset_sql_stats()")

	row = db.QueryRow(t, "SELECT count_rows() FROM transaction_activity")
	row.Scan(&count)
	require.Zero(t, count, "transaction_activity after transfer: expect:0, actual:%d", count)

	row = db.QueryRow(t, "SELECT count_rows() FROM statement_activity")
	row.Scan(&count)
	require.Zero(t, count, "statement_activity after transfer: expect:0, actual:%d", count)

	row = db.QueryRow(t, "SELECT count_rows() FROM crdb_internal.transaction_activity")
	row.Scan(&count)
	require.Zero(t, count, "crdb_internal.transaction_activity after transfer: expect:0, actual:%d", count)

	row = db.QueryRow(t, "SELECT count_rows() FROM crdb_internal.statement_activity")
	row.Scan(&count)
	require.Zero(t, count, "crdb_internal.statement_activity after transfer: expect:0, actual:%d", count)
}

// TestSqlActivityUpdateTopLimitJob verifies that the
// job is created.
func TestSqlActivityUpdateTopLimitJob(t *testing.T) {
	defer leaktest.AfterTest(t)()
	defer log.Scope(t).Close(t)

	stubTime := timeutil.Now().Truncate(time.Hour)
	sqlStatsKnobs := sqlstats.CreateTestingKnobs()
	sqlStatsKnobs.StubTimeNow = func() time.Time { return stubTime }

	// Start the cluster.
	ctx := context.Background()
	srv, sqlDB, _ := serverutils.StartServer(t, base.TestServerArgs{
		Insecure: true,
		Knobs: base.TestingKnobs{
			SQLStatsKnobs:    sqlStatsKnobs,
			JobsTestingKnobs: jobs.NewTestingKnobsWithShortIntervals(),
		},
	})
	defer srv.Stopper().Stop(context.Background())
	defer sqlDB.Close()

	db := sqlutils.MakeSQLRunner(sqlDB)
	db.Exec(t, "SET database = crdb_internal.current_observability_database()")

	// Give permission to write to sys tables.
	db.Exec(t, "INSERT INTO system.users VALUES ('node', NULL, true, 3)")
	db.Exec(t, "GRANT node TO root")

	// Make sure all the tables are empty initially.
	db.Exec(t, "DELETE FROM transaction_activity")
	db.Exec(t, "DELETE FROM statement_activity")
	db.Exec(t, "DELETE FROM transaction_statistics")
	db.Exec(t, "DELETE FROM statement_statistics")

	execCfg := srv.ExecutorConfig().(ExecutorConfig)
	st := cluster.MakeTestingClusterSettings()
	su := st.MakeUpdater()
	const topLimit = 3
	err := su.Set(ctx, "sql.stats.activity.top.max", settings.EncodedValue{
		Value: settings.EncodeInt(int64(topLimit)),
		Type:  "i",
	})
	require.NoError(t, err)

	updater := newSqlActivityUpdater(st, execCfg.InternalDB, sqlStatsKnobs)

	db.Exec(t, "SET tracing = true;")

	db.Exec(t, "set cluster setting sql.txn_stats.sample_rate  = 1;")

	const appNamePrefix = "TestSqlActivityUpdateJobLoop"
	getAppName := func(count int) string {
		return fmt.Sprintf("%s%d", appNamePrefix, count)
	}
	appIndexCount := 0
	updateStatsCount := 0
	for i := 1; i <= 5; i++ {

		// Generate unique rows in the statistics tables.
		// numQueries = per-column-limit * numColumns with some padding since we need more rows than the limit.
		const numQueries = topLimit*6 + 10
		for j := 0; j < numQueries; j++ {
			appIndexCount++
			db.Exec(t, "SET SESSION application_name=$1", getAppName(appIndexCount))
			db.Exec(t, "SELECT 1;")
		}

		db.Exec(t, "SET SESSION application_name=$1", "randomIgnore")

		srv.SQLServer().(*Server).GetSQLStatsProvider().(*persistedsqlstats.PersistedSQLStats).Flush(ctx)

		// The max number of queries is number of top columns * max number of
		// queries per a column (6*3=18 for this test, 6*500=3000 default). Most of
		// the top queries are the same in this test so instead of getting 18 it's
		// only 6 to 8 which make test unreliable. The solution is to change the
		// values for each of the columns to make sure that it is always the max.
		// Then run the update multiple times with new top queries each time. This
		// is needed to simulate ORM which generates a lot of unique queries.

		// Execution count.
		for j := 0; j < topLimit; j++ {
			updateStatsCount++
			db.Exec(t, `UPDATE statement_statistics
			SET statistics =  jsonb_set(jsonb_set(statistics, '{execution_statistics, cnt}', to_jsonb($1::INT)),
			    '{statistics, svcLat, mean}', to_jsonb($2::FLOAT))
			    WHERE app_name = $3;`, 10000+updateStatsCount, 0.0000001, getAppName(updateStatsCount))
		}

		// Service latency time.
		for j := 0; j < topLimit; j++ {
			updateStatsCount++
			db.Exec(t, `UPDATE statement_statistics
			SET statistics =  jsonb_set(jsonb_set(statistics, '{execution_statistics, cnt}', to_jsonb($1::INT)),
			    '{statistics, svcLat, mean}', to_jsonb($2::FLOAT))
			    WHERE app_name = $3;`, 1, 1000+updateStatsCount, getAppName(updateStatsCount))
		}

		// Total execution time. Needs to be less than individual execution count
		// and service latency, but greater when multiplied together.
		for j := 0; j < topLimit; j++ {
			updateStatsCount++
			db.Exec(t, `UPDATE statement_statistics
			SET statistics =  jsonb_set(jsonb_set(statistics, '{execution_statistics, cnt}', to_jsonb($1::INT)),
			    '{statistics, svcLat, mean}', to_jsonb($2::FLOAT))
			    WHERE app_name = $3;`, 500+updateStatsCount, 500+updateStatsCount, getAppName(updateStatsCount))
		}

		// Remaining columns don't interact so a loop can be used
		columnsToChangeValues := []string{"{execution_statistics, contentionTime, mean}", "{execution_statistics, cpu_sql_nanos, mean}", "{statistics, latencyInfo, p99}"}
		for _, updateField := range columnsToChangeValues {
			for j := 0; j < topLimit; j++ {
				updateStatsCount++
				db.Exec(t, `UPDATE statement_statistics
			SET statistics =  jsonb_set(statistics, $1, to_jsonb($2::INT)) 
			WHERE app_name = $3;`, updateField, 10000+updateStatsCount, getAppName(updateStatsCount))
			}
		}

		// Run the updater to add rows to the activity tables.
		// This will use the transfer all scenarios with there only
		// being a few rows.
		err = updater.TransferStatsToActivity(ctx)
		require.NoError(t, err)

		maxRows := topLimit * 6 // Number of top columns to select from.
		row := db.QueryRow(t,
			`SELECT count_rows() FROM transaction_activity WHERE app_name LIKE 'TestSqlActivityUpdateJobLoop%'`)
		var count int
		row.Scan(&count)
		require.LessOrEqual(t, count, maxRows, "transaction_activity after transfer: actual:%d, max:%d", count, maxRows)

		row = db.QueryRow(t,
			`SELECT count_rows() FROM statement_activity WHERE app_name LIKE 'TestSqlActivityUpdateJobLoop%'`)
		row.Scan(&count)
		require.LessOrEqual(t, count, maxRows, "statement_activity after transfer: actual:%d, max:%d", count, maxRows)

		row = db.QueryRow(t, `SELECT count_rows() FROM transaction_activity`)
		row.Scan(&count)
		require.LessOrEqual(t, count, maxRows, "transaction_activity after transfer: actual:%d, max:%d", count, maxRows)
	}
}

func TestSqlActivityJobRunsAfterStatsFlush(t *testing.T) {
	defer leaktest.AfterTest(t)()
	defer log.Scope(t).Close(t)

	// Start the cluster.
	ctx := context.Background()
	st := cluster.MakeTestingClusterSettings()
	srv, db, _ := serverutils.StartServer(t, base.TestServerArgs{
		Insecure: true,
		Settings: st,
		Knobs: base.TestingKnobs{
			SQLStatsKnobs:    sqlstats.CreateTestingKnobs(),
			JobsTestingKnobs: jobs.NewTestingKnobsWithShortIntervals(),
		},
	})
	defer srv.Stopper().Stop(context.Background())
	defer db.Close()

	_, err := db.ExecContext(ctx, "SET database = crdb_internal.current_observability_database()")
	require.NoError(t, err)

	_, err = db.ExecContext(ctx, "SET CLUSTER SETTING sql.stats.flush.interval = '100ms'")
	require.NoError(t, err)
	appName := "TestScheduledSQLStatsCompaction"
	_, err = db.ExecContext(ctx, "SET SESSION application_name=$1", appName)
	require.NoError(t, err)

	testutils.SucceedsWithin(t, func() error {
		_, err = db.ExecContext(ctx, "SELECT 1;")
		require.NoError(t, err)

		row := db.QueryRowContext(ctx, "SELECT count_rows() "+
			"FROM transaction_activity WHERE app_name = $1", appName)
		var count int
		err = row.Scan(&count)
		if err != nil {
			return err
		}
		if count <= 0 {
			return fmt.Errorf("transaction_activity is empty: %d", count)
		}

		row = db.QueryRowContext(ctx, "SELECT count_rows() "+
			"FROM statement_activity WHERE app_name = $1", appName)
		err = row.Scan(&count)
		if err != nil {
			return err
		}
		if count <= 0 {
			return fmt.Errorf("statement_activity is empty: %d", count)
		}

		return nil
	}, 1*time.Minute)
}

// TestTransactionActivityMetadata verifies the metadata JSON column of transaction_activity are
// what we expect it to be. This test was added to address #103618.
func TestTransactionActivityMetadata(t *testing.T) {
	defer leaktest.AfterTest(t)()
	defer log.Scope(t).Close(t)

	ctx := context.Background()
	stubTime := timeutil.Now().Truncate(time.Hour)
	sqlStatsKnobs := sqlstats.CreateTestingKnobs()
	sqlStatsKnobs.StubTimeNow = func() time.Time { return stubTime }
	s, sqlDB, _ := serverutils.StartServer(t, base.TestServerArgs{
		Insecure: true,
		Knobs: base.TestingKnobs{
			SQLStatsKnobs: sqlStatsKnobs,
			UpgradeManager: &upgradebase.TestingKnobs{
				DontUseJobs:                       true,
				SkipUpdateSQLActivityJobBootstrap: true,
			},
		},
	})
	defer s.Stopper().Stop(context.Background())
	defer sqlDB.Close()

	execCfg := s.ExecutorConfig().(ExecutorConfig)
	st := cluster.MakeTestingClusterSettings()
	updater := newSqlActivityUpdater(st, execCfg.InternalDB, sqlStatsKnobs)

	db := sqlutils.MakeSQLRunner(sqlDB)
	db.Exec(t, "SET SESSION application_name = 'test_txn_activity_table'")

	// Generate some sql stats data.
	db.Exec(t, "SELECT 1;")

	// Flush and transfer stats.
	var metadataJSON string
	s.SQLServer().(*Server).GetSQLStatsProvider().(*persistedsqlstats.PersistedSQLStats).Flush(ctx)

	// Ensure that the metadata column contains the populated 'stmtFingerprintIDs' field.
	var metadata struct {
		StmtFingerprintIDs []string `json:"stmtFingerprintIDs,"`
	}

	require.NoError(t, updater.TransferStatsToActivity(ctx))
	db.QueryRow(t, "SELECT metadata FROM transaction_activity LIMIT 1").Scan(&metadataJSON)
	require.NoError(t, json.Unmarshal([]byte(metadataJSON), &metadata))
	require.NotEmpty(t, metadata.StmtFingerprintIDs)

	// Do the same but testing transferTopStats.
	db.Exec(t, "SELECT crdb_internal.reset_sql_stats()")
	db.Exec(t, "SELECT 1")

	// Flush and transfer top stats.
	s.SQLServer().(*Server).GetSQLStatsProvider().(*persistedsqlstats.PersistedSQLStats).Flush(ctx)
	require.NoError(t, updater.transferTopStats(ctx, stubTime, 100, 100, 100))

	// Ensure that the metadata column contains the populated 'stmtFingerprintIDs' field.
	db.QueryRow(t, "SELECT metadata FROM transaction_activity LIMIT 1").Scan(&metadataJSON)
	require.NoError(t, json.Unmarshal([]byte(metadataJSON), &metadata))
	require.NotEmpty(t, metadata.StmtFingerprintIDs)
}

// Verify the cluster setting ignores activity tables when disabled
// 1. Changes app name and execute 2 queries (select _, change app name)
// 2. Check results include the app which should be from activity tables
// 3. Change the app names in the activity tables
// 4. Verify original app name no longer found
// 5. Disable cluster setting
// 6. Verify original app name in result from statistics table
func TestActivityStatusCombineAPI(t *testing.T) {
	defer leaktest.AfterTest(t)()
	defer log.Scope(t).Close(t)

	ctx := context.Background()
	stubTime := timeutil.Now().Truncate(time.Hour)
	sqlStatsKnobs := sqlstats.CreateTestingKnobs()
	sqlStatsKnobs.StubTimeNow = func() time.Time { return stubTime }
	s, sqlDB, _ := serverutils.StartServer(t, base.TestServerArgs{
		Insecure: true,
		Knobs: base.TestingKnobs{
			SQLStatsKnobs: sqlStatsKnobs,
			UpgradeManager: &upgradebase.TestingKnobs{
				DontUseJobs:                       true,
				SkipUpdateSQLActivityJobBootstrap: true,
			},
		},
	})
	defer s.Stopper().Stop(context.Background())
	defer sqlDB.Close()

	execCfg := s.ExecutorConfig().(ExecutorConfig)
	st := cluster.MakeTestingClusterSettings()
	updater := newSqlActivityUpdater(st, execCfg.InternalDB, sqlStatsKnobs)

	db := sqlutils.MakeSQLRunner(sqlDB)
	db.Exec(t, "SET database = crdb_internal.current_observability_database()")

	// Generate a random app name each time to avoid conflicts
	appName := "test_status_api" + uuid.FastMakeV4().String()
	db.Exec(t, "SET SESSION application_name = $1", appName)

	// Generate some sql stats data.
	db.Exec(t, "SELECT 1;")

	// Switch the app name back so any queries ran after do not get included
	db.Exec(t, "SET SESSION application_name = '$ internal-test'")

	// Flush and transfer stats.
	var metadataJSON string
	s.SQLServer().(*Server).GetSQLStatsProvider().(*persistedsqlstats.PersistedSQLStats).Flush(ctx)

	// Ensure that the metadata column contains the populated 'stmtFingerprintIDs' field.
	var metadata struct {
		StmtFingerprintIDs []string `json:"stmtFingerprintIDs,"`
	}

	require.NoError(t, updater.TransferStatsToActivity(ctx))
	db.QueryRow(t, "SELECT metadata FROM transaction_activity LIMIT 1").Scan(&metadataJSON)
	require.NoError(t, json.Unmarshal([]byte(metadataJSON), &metadata))
	require.NotEmpty(t, metadata.StmtFingerprintIDs)

	// Hit query endpoint.
	start := stubTime
	end := stubTime
	var resp serverpb.StatementsResponse
	if err := getStatusJSONProto(s, "combinedstmts", &resp, start, end); err != nil {
		t.Fatal(err)
	}
	require.NotEmpty(t, resp.Transactions)
	require.NotEmpty(t, resp.Statements)

	stmtAppNameCnt := getStmtAppNameCount(resp, appName)
	require.Greater(t, stmtAppNameCnt, 0)

	txnAppNameCnt := getTxnAppNameCnt(resp, appName)
	require.Greater(t, txnAppNameCnt, 0)

	// Grant permission and change the activity table info
	db.Exec(t, "INSERT INTO system.users VALUES ('node', NULL, true, 3)")
	db.Exec(t, "GRANT node TO root")
	db.Exec(t, "UPDATE statement_activity SET app_name = 'randomapp' where app_name = $1;", appName)
	db.Exec(t, "UPDATE transaction_activity SET app_name = 'randomapp' where app_name = $1;", appName)

	if err := getStatusJSONProto(s, "combinedstmts", &resp, start, end); err != nil {
		t.Fatal(err)
	}
	// Verify the activity table changes caused the response to change.
	require.NotEmpty(t, resp.Transactions)
	require.NotEmpty(t, resp.Statements)
	appNameChangedStmtCnt := getStmtAppNameCount(resp, appName)
	require.Equal(t, 0, appNameChangedStmtCnt)

	appNameChangedTxnCnt := getTxnAppNameCnt(resp, appName)
	require.Equal(t, 0, appNameChangedTxnCnt)

	// Disable the activity ui cluster setting so it only pull from stats tables
	db.Exec(t, "set cluster setting sql.stats.activity.ui.enabled = false;")

	if err := getStatusJSONProto(s, "combinedstmts", &resp, start, end); err != nil {
		t.Fatal(err)
	}
	require.NotEmpty(t, resp.Transactions)
	require.NotEmpty(t, resp.Statements)

	// These should be the same as the original results.
	uiDisabledStmtAppNameCnt := getStmtAppNameCount(resp, appName)
	require.Equal(t, stmtAppNameCnt, uiDisabledStmtAppNameCnt)
	uiDisabledTxnAppNameCnt := getTxnAppNameCnt(resp, appName)
	require.Equal(t, txnAppNameCnt, uiDisabledTxnAppNameCnt)
}

func getTxnAppNameCnt(resp serverpb.StatementsResponse, appName string) int {
	txnAppNameCnt := 0
	for _, txn := range resp.Transactions {
		if txn.StatsData.App == appName {
			txnAppNameCnt++
		}
	}
	return txnAppNameCnt
}

func getStmtAppNameCount(resp serverpb.StatementsResponse, appName string) int {
	stmtAppNameCnt := 0
	for _, stmt := range resp.Statements {
		if stmt.Key.KeyData.App == appName {
			stmtAppNameCnt++
		}
	}
	return stmtAppNameCnt
}

func getStatusJSONProto(
	ts serverutils.TestServerInterface,
	path string,
	response protoutil.Message,
	startTime time.Time,
	endTime time.Time,
) error {
	url := fmt.Sprintf("/_status/%s?start=%d&end=%d", path, startTime.Unix(), endTime.Unix())
	return serverutils.GetJSONProto(ts, url, response)
}
