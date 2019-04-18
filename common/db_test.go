package common

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestDB(t *testing.T) {
	db, err := NewDBClient(context.Background(), "test")
	assert.NoError(t, err)
	err = db.Close()
	assert.NoError(t, err)
}

func TestAlertDB(t *testing.T) {
	db, err := NewDBClient(context.Background(), "test")
	assert.NoError(t, err)

	id := "1234567890"
	a := &Alert{
		Id:        id,
		Timestamp: time.Now().Add(time.Duration(-5) * time.Minute),
		Metadata: []*AlertMeta{
			{Key: "status", Value: ALERT_NEW},
			{Key: "foo", Value: "bar"},
		},
	}
	err = db.SaveAlert(context.Background(), a)
	assert.NoError(t, err)

	na, err := db.GetAlert(context.Background(), id)
	assert.NoError(t, err)
	assert.Equal(t, a.Id, na.Id)
	assert.Equal(t, a.Metadata, na.Metadata)
	assert.True(t, a.Timestamp.Equal(na.Timestamp))

	na.SetMetadata("status", ALERT_ESCALATED)
	err = db.SaveAlert(context.Background(), na)
	assert.NoError(t, err)
	nna, err := db.GetAlert(context.Background(), id)
	assert.NoError(t, err)
	assert.True(t, nna.IsStatus(ALERT_ESCALATED))
	assert.True(t, a.Timestamp.Equal(nna.Timestamp))

	alerts, err := db.GetAllAlerts(context.Background())
	assert.NoError(t, err)
	assert.True(t, 1 == len(alerts))
	assert.Equal(t, a.Id, alerts[0].Id)
	assert.True(t, a.Timestamp.Equal(alerts[0].Timestamp))
	assert.Equal(t, nna.Metadata, alerts[0].Metadata)

	err = db.Close()
	assert.NoError(t, err)
}

func TestWhitelistedIpDB(t *testing.T) {
	db, err := NewDBClient(context.Background(), "test")
	assert.NoError(t, err)

	wip := NewWhitelistedIP("127.0.0.1", time.Now().Add(time.Hour), "test")

	err = db.SaveWhitelistedIp(context.Background(), wip)
	assert.NoError(t, err)

	wips, err := db.GetAllWhitelistedIps(context.Background())
	assert.NoError(t, err)
	assert.True(t, 1 == len(wips))
	assert.True(t, WIPEqual(wip, wips[0]))

	expiredWip := NewWhitelistedIP("127.0.0.2", time.Now().Add(time.Duration(-1)*time.Hour), "test")
	err = db.SaveWhitelistedIp(context.Background(), expiredWip)
	assert.NoError(t, err)
	wips, err = db.GetAllWhitelistedIps(context.Background())
	assert.NoError(t, err)
	assert.True(t, 2 == len(wips))

	err = db.RemoveExpiredWhitelistedIps(context.Background())
	assert.NoError(t, err)

	wips, err = db.GetAllWhitelistedIps(context.Background())
	assert.NoError(t, err)
	assert.True(t, 1 == len(wips))
	assert.True(t, WIPEqual(wip, wips[0]))

	err = db.DeleteWhitelistedIp(context.Background(), wip)
	assert.NoError(t, err)

	err = db.Close()
	assert.NoError(t, err)
}

func WIPEqual(wipOne, wipTwo *WhitelistedIP) bool {
	if wipOne.IP != wipTwo.IP {
		return false
	}
	if wipOne.CreatedBy != wipTwo.CreatedBy {
		return false
	}
	if !wipOne.ExpiresAt.Equal(wipTwo.ExpiresAt) {
		return false
	}
	return true
}
