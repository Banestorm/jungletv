package server

import (
	"context"
	"encoding/json"
	"math/big"
	"time"

	"github.com/palantir/stacktrace"
	"github.com/tnyim/jungletv/proto"
	"github.com/tnyim/jungletv/utils/event"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// MediaQueueEntry represents one entry in the media queue
type MediaQueueEntry interface {
	json.Marshaler
	json.Unmarshaler
	RequestedBy() User
	RequestCost() Amount
	RequestedAt() time.Time
	Unskippable() bool
	MediaInfo() MediaInfo
	SerializeForAPI(ctx context.Context, userSerializer APIUserSerializer) *proto.QueueEntry
	ProduceCheckpointForAPI(ctx context.Context, userSerializer APIUserSerializer, needsTitle bool) *proto.MediaConsumptionCheckpoint
	Play()
	Stop()
	Played() bool
	Playing() bool
	PlayedFor() time.Duration
	DonePlaying() *event.Event

	QueueID() string
}

type MediaInfo interface {
	Title() string
	ThumbnailURL() string
	Offset() time.Duration
	Length() time.Duration
	ProduceMediaQueueEntry(requestedBy User, requestCost Amount, unskippable bool, queueID string) MediaQueueEntry
	FillAPITicketMediaInfo(ticket *proto.EnqueueMediaTicket)
}

type queueEntryYouTubeVideo struct {
	queueID       string
	id            string
	title         string
	channelTitle  string
	thumbnailURL  string
	duration      time.Duration
	offset        time.Duration
	unskippable   bool
	liveBroadcast bool

	requestedBy    User
	requestCost    Amount
	requestedAt    time.Time
	startedPlaying time.Time
	stoppedPlaying time.Time
	played         bool
	donePlaying    *event.Event
}

func (e *queueEntryYouTubeVideo) ProduceMediaQueueEntry(requestedBy User, requestCost Amount, unskippable bool, queueID string) MediaQueueEntry {
	e.requestedBy = requestedBy
	e.requestCost = requestCost
	e.unskippable = unskippable
	e.queueID = queueID
	e.requestedAt = time.Now()
	return e
}

func (e *queueEntryYouTubeVideo) QueueID() string {
	return e.queueID
}

func (e *queueEntryYouTubeVideo) Title() string {
	return e.title
}

func (e *queueEntryYouTubeVideo) ThumbnailURL() string {
	return e.thumbnailURL
}

func (e *queueEntryYouTubeVideo) Length() time.Duration {
	return e.duration
}

func (e *queueEntryYouTubeVideo) Offset() time.Duration {
	return e.offset
}

func (e *queueEntryYouTubeVideo) MediaInfo() MediaInfo {
	return e
}

func (e *queueEntryYouTubeVideo) RequestedBy() User {
	return e.requestedBy
}

func (e *queueEntryYouTubeVideo) RequestCost() Amount {
	return e.requestCost
}

func (e *queueEntryYouTubeVideo) RequestedAt() time.Time {
	return e.requestedAt
}

func (e *queueEntryYouTubeVideo) Unskippable() bool {
	return e.unskippable
}

func (e *queueEntryYouTubeVideo) SerializeForAPI(ctx context.Context, userSerializer APIUserSerializer) *proto.QueueEntry {
	entry := &proto.QueueEntry{
		Id:          e.queueID,
		Length:      durationpb.New(e.duration),
		Offset:      durationpb.New(e.offset),
		Unskippable: e.unskippable,
		RequestCost: e.requestCost.SerializeForAPI(),
		RequestedAt: timestamppb.New(e.requestedAt),
		MediaInfo: &proto.QueueEntry_YoutubeVideoData{
			YoutubeVideoData: &proto.QueueYouTubeVideoData{
				Id:            e.id,
				Title:         e.title,
				ThumbnailUrl:  e.thumbnailURL,
				ChannelTitle:  e.channelTitle,
				LiveBroadcast: e.liveBroadcast,
			},
		},
	}
	if !e.requestedBy.IsUnknown() {
		entry.RequestedBy = userSerializer(ctx, e.requestedBy)
	}
	return entry
}

type queueEntryYouTubeVideoJsonRepresentation struct {
	QueueID       string
	Type          string
	ID            string
	Title         string
	ChannelTitle  string
	ThumbnailURL  string
	Duration      time.Duration
	Offset        time.Duration
	LiveBroadcast bool
	RequestedBy   string
	RequestCost   *big.Int
	RequestedAt   time.Time
	Unskippable   bool
}

func (e *queueEntryYouTubeVideo) MarshalJSON() ([]byte, error) {
	j, err := json.Marshal(queueEntryYouTubeVideoJsonRepresentation{
		QueueID:       e.queueID,
		Type:          "youtube-video",
		ID:            e.id,
		Title:         e.title,
		ChannelTitle:  e.channelTitle,
		ThumbnailURL:  e.thumbnailURL,
		Duration:      e.duration,
		Offset:        e.offset,
		LiveBroadcast: e.liveBroadcast,
		RequestedBy:   e.requestedBy.Address(),
		RequestCost:   e.requestCost.Int,
		RequestedAt:   e.requestedAt,
		Unskippable:   e.unskippable,
	})
	if err != nil {
		return nil, stacktrace.Propagate(err, "error serializing queue entry %s", e.id)
	}
	return j, nil
}

func (e *queueEntryYouTubeVideo) UnmarshalJSON(b []byte) error {
	var t queueEntryYouTubeVideoJsonRepresentation
	if err := json.Unmarshal(b, &t); err != nil {
		return stacktrace.Propagate(err, "error deserializing queue entry")
	}

	e.queueID = t.QueueID
	e.id = t.ID
	e.title = t.Title
	e.channelTitle = t.ChannelTitle
	e.thumbnailURL = t.ThumbnailURL
	e.duration = t.Duration
	e.offset = t.Offset
	e.liveBroadcast = t.LiveBroadcast
	e.requestedBy = NewAddressOnlyUser(t.RequestedBy)
	e.requestCost = Amount{t.RequestCost}
	e.requestedAt = t.RequestedAt
	e.unskippable = t.Unskippable
	e.donePlaying = event.New()
	return nil
}

func (e *queueEntryYouTubeVideo) FillAPITicketMediaInfo(ticket *proto.EnqueueMediaTicket) {
	ticket.MediaLength = durationpb.New(e.duration)
	ticket.MediaInfo = &proto.EnqueueMediaTicket_YoutubeVideoData{
		YoutubeVideoData: &proto.QueueYouTubeVideoData{
			Id:            e.id,
			Title:         e.title,
			ChannelTitle:  e.channelTitle,
			ThumbnailUrl:  e.thumbnailURL,
			LiveBroadcast: e.liveBroadcast,
		},
	}
}

func (e *queueEntryYouTubeVideo) ProduceCheckpointForAPI(ctx context.Context, userSerializer APIUserSerializer, needsTitle bool) *proto.MediaConsumptionCheckpoint {
	cp := &proto.MediaConsumptionCheckpoint{
		MediaPresent:    true,
		CurrentPosition: durationpb.New(e.Offset() + e.PlayedFor()),
		LiveBroadcast:   e.liveBroadcast,
		RequestCost:     e.requestCost.SerializeForAPI(),
		// Reward is optionally filled outside this function
		MediaInfo: &proto.MediaConsumptionCheckpoint_YoutubeVideoData{
			YoutubeVideoData: &proto.NowPlayingYouTubeVideoData{
				Id: e.id,
			},
		},
	}
	if needsTitle {
		cp.MediaTitle = &e.title
	}
	if !e.requestedBy.IsUnknown() {
		cp.RequestedBy = userSerializer(ctx, e.requestedBy)
	}
	return cp
}

func (e *queueEntryYouTubeVideo) Play() {
	e.startedPlaying = time.Now()
	c := time.NewTimer(e.duration).C
	go func() {
		<-c
		if e.Playing() {
			e.played = true
			e.donePlaying.Notify()
		}
	}()
}

func (e *queueEntryYouTubeVideo) Played() bool {
	return e.played
}

func (e *queueEntryYouTubeVideo) Stop() {
	if !e.Playing() {
		return
	}
	e.played = true
	e.stoppedPlaying = time.Now()
	e.donePlaying.Notify()
}

func (e *queueEntryYouTubeVideo) Playing() bool {
	return !e.startedPlaying.IsZero() && !e.played
}

func (e *queueEntryYouTubeVideo) PlayedFor() time.Duration {
	if !e.Playing() {
		return e.stoppedPlaying.Sub(e.startedPlaying)
	}
	return time.Now().Sub(e.startedPlaying)
}

func (e *queueEntryYouTubeVideo) DonePlaying() *event.Event {
	return e.donePlaying
}
