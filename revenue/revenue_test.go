package revenue

import (
	"errors"
	"reflect"
	"testing"

	"github.com/lightningnetwork/lnd/lnrpc"
	"github.com/lightningnetwork/lnd/lnwire"
	"github.com/stretchr/testify/require"
)

// TestGetRevenueReport tests querying for a revenue report.
func TestGetRevenueReport(t *testing.T) {
	var (
		// testErr is an error returned by the mock to simulate rpc
		// failures.
		testErr = errors.New("error thrown by mock")

		chan1 = &lnrpc.Channel{
			ChannelPoint: "a:1",
			ChanId:       123,
		}

		chan2 = &lnrpc.Channel{
			ChannelPoint: "a:2",
			ChanId:       321,
		}
	)

	tests := []struct {
		name           string
		listChanErr    error
		closedChanErr  error
		forwardHistErr error
		openChannels   []*lnrpc.Channel
		closedChannels []*lnrpc.ChannelCloseSummary
		fwdHistory     []*lnrpc.ForwardingEvent
		expectedReport *Report
		expectErr      error
	}{
		{
			name:        "open channels fails",
			listChanErr: testErr,
			expectErr:   testErr,
		},
		{
			name:          "closed channels fails",
			closedChanErr: testErr,
			expectErr:     testErr,
		},
		{
			name:           "forward history fails",
			forwardHistErr: testErr,
			expectErr:      testErr,
		},
		{
			name: "cannot find channel",
			fwdHistory: []*lnrpc.ForwardingEvent{
				{
					ChanIdIn: 123,
				},
			},
			expectErr: nil,
			expectedReport: &Report{
				ChannelPairs: make(map[string]map[string]Revenue),
			},
		},
		{
			name:         "open and closed channel",
			openChannels: []*lnrpc.Channel{chan1},
			closedChannels: []*lnrpc.ChannelCloseSummary{{
				ChannelPoint: chan2.ChannelPoint,
				ChanId:       chan2.ChanId,
			}},
			fwdHistory: []*lnrpc.ForwardingEvent{
				{
					ChanIdIn:   chan1.ChanId,
					ChanIdOut:  chan2.ChanId,
					AmtOutMsat: 100,
					AmtInMsat:  150,
				},
			},
			expectedReport: &Report{
				ChannelPairs: map[string]map[string]Revenue{
					chan1.ChannelPoint: {
						chan2.ChannelPoint: Revenue{
							AmountIncoming: 150,
							AmountOutgoing: 0,
							FeesIncoming:   50,
							FeesOutgoing:   0,
						}},
					chan2.ChannelPoint: {
						chan1.ChannelPoint: Revenue{
							AmountIncoming: 0,
							AmountOutgoing: 100,
							FeesIncoming:   0,
							FeesOutgoing:   50,
						}},
				}},
			expectErr: nil,
		},
	}

	for _, test := range tests {
		test := test

		t.Run(test.name, func(t *testing.T) {
			// Create a config which returns the tests's specified
			// responses and errors.
			cfg := &Config{
				ListChannels: func() ([]*lnrpc.Channel, error) {
					return test.openChannels, test.listChanErr
				},
				ClosedChannels: func() ([]*lnrpc.ChannelCloseSummary, error) {
					return test.closedChannels, test.closedChanErr
				},
				ForwardingHistory: func(offset,
					max uint32) ([]*lnrpc.ForwardingEvent, uint32, error) {

					return test.fwdHistory, offset, test.forwardHistErr
				},
			}

			report, err := GetRevenueReport(cfg)
			if test.expectErr != err {
				t.Fatalf("expected: %v, got: %v",
					test.expectErr, err)
			}

			if !reflect.DeepEqual(test.expectedReport, report) {
				t.Fatalf("expected: \n%+v, got: \n%+v",
					test.expectedReport, report)
			}

		})
	}
}

// TestGetEvents tests fetching of forwarding events and lookup of our channel
// point based on short channel ID. It tests cases where the lookup succeeds,
// and where it fails and we are expected to skip the event. It does not test
// pagination because that functionality is covered by the pagination package.
func TestGetEvents(t *testing.T) {
	chanInID := lnwire.NewShortChanIDFromInt(123)
	chanOutID := lnwire.NewShortChanIDFromInt(321)

	// mockedEvents is the set of events our mock returns.
	mockedEvents := []*lnrpc.ForwardingEvent{
		{
			ChanIdIn:   chanInID.ToUint64(),
			ChanIdOut:  chanOutID.ToUint64(),
			AmtOutMsat: 2000,
			AmtInMsat:  4000,
		},
	}

	// mockQuery returns our set of mocked events.
	mockQuery := func(_, _ uint32) ([]*lnrpc.ForwardingEvent, uint32,
		error) {

		return mockedEvents, 0, nil
	}

	// channelIDFound is a map that will successfully lookup an outpoint for
	// out mocked events channels.
	var chanInOutpoint, chanOutOutpoint = "a:1", "b:1"
	channelIDFound := map[lnwire.ShortChannelID]string{
		chanInID:  chanInOutpoint,
		chanOutID: chanOutOutpoint,
	}

	events, err := getEvents(channelIDFound, mockQuery)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// expectedEvents is the set of events we expect to get when we can
	// lookup all our channels.
	expectedEvents := []revenueEvent{
		{
			incomingChannel: chanInOutpoint,
			outgoingChannel: chanOutOutpoint,
			incomingAmt:     events[0].incomingAmt,
			outgoingAmt:     events[0].outgoingAmt,
		},
	}

	require.Equal(t, expectedEvents, events)

	// Now, we make a query with an empty channel map (which means we cannot
	// lookup the mapping from short channel ID to channel point). We expect
	// getEvents to skip this event and succeed with an empty set of events.
	channelNotFound := make(map[lnwire.ShortChannelID]string)
	events, err = getEvents(channelNotFound, mockQuery)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	require.Len(t, events, 0)
}

// TestGetReport tests creation of a revenue report for a set of
// revenue events. It covers the case where there are no events, and
// the case where one channel is involved in multiple forwards.
func TestGetReport(t *testing.T) {
	var (
		channel1 = "a:1"
		channel2 = "a:2"
	)

	// chan1Incoming is a forwarding event where channel 1 is the incoming
	// channel.
	chan1Incoming := revenueEvent{
		incomingChannel: channel1,
		outgoingChannel: channel2,
		incomingAmt:     1000,
		outgoingAmt:     500,
	}

	// chan1Outgoing is a forwarding event where channel1 is the outgoing
	// channel.
	chan1Outgoing := revenueEvent{
		incomingChannel: channel2,
		outgoingChannel: channel1,
		incomingAmt:     400,
		outgoingAmt:     200,
	}

	// chan2Event is a forwarding event that channel1 is not involved in.
	chan2Event := revenueEvent{
		incomingChannel: channel2,
		outgoingChannel: channel2,
		incomingAmt:     100,
		outgoingAmt:     90,
	}

	tests := []struct {
		name           string
		events         []revenueEvent
		expectedReport *Report
	}{
		{
			name:   "no events",
			events: []revenueEvent{},
			expectedReport: &Report{
				ChannelPairs: make(map[string]map[string]Revenue),
			},
		},
		{
			name: "multiple forwards for one channel",
			events: []revenueEvent{
				chan1Incoming,
				chan1Outgoing,
				chan2Event,
			},
			expectedReport: &Report{
				ChannelPairs: map[string]map[string]Revenue{
					channel1: {
						channel2: {
							AmountOutgoing: 200,
							AmountIncoming: 1000,
							FeesOutgoing:   200,
							FeesIncoming:   500,
						},
					},
					channel2: {
						channel1: {
							AmountOutgoing: 500,
							AmountIncoming: 400,
							FeesOutgoing:   500,
							FeesIncoming:   200,
						},
						channel2: {
							AmountOutgoing: 90,
							AmountIncoming: 100,
							FeesOutgoing:   10,
							FeesIncoming:   10,
						},
					},
				},
			},
		},
	}

	for _, test := range tests {
		test := test

		t.Run(test.name, func(t *testing.T) {
			report := getReport(test.events)

			if !reflect.DeepEqual(report, test.expectedReport) {
				t.Fatalf("expected revenue: %v, got: %v",
					test.expectedReport, report)
			}
		})
	}
}
