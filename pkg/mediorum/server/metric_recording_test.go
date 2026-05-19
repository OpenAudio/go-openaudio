package server

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRecordMetricConcurrentIncrements(t *testing.T) {
	ss := testNetwork[0]
	action := fmt.Sprintf("test_record_metric_%d", time.Now().UnixNano())
	today := time.Now().UTC().Truncate(24 * time.Hour)
	firstOfMonth := time.Date(today.Year(), today.Month(), 1, 0, 0, 0, 0, time.UTC)

	require.NoError(t, ss.crud.DB.Where("action = ?", action).Delete(&DailyMetrics{}).Error)
	require.NoError(t, ss.crud.DB.Where("action = ?", action).Delete(&MonthlyMetrics{}).Error)

	const writers = 64
	var wg sync.WaitGroup
	start := make(chan struct{})
	wg.Add(writers)
	for i := 0; i < writers; i++ {
		go func() {
			defer wg.Done()
			<-start
			ss.recordMetric(action)
		}()
	}
	close(start)
	wg.Wait()

	var daily DailyMetrics
	require.NoError(t, ss.crud.DB.First(&daily, "timestamp = ? AND action = ?", today, action).Error)
	assert.EqualValues(t, writers, daily.Count)

	var monthly MonthlyMetrics
	require.NoError(t, ss.crud.DB.First(&monthly, "timestamp = ? AND action = ?", firstOfMonth, action).Error)
	assert.EqualValues(t, writers, monthly.Count)
}
