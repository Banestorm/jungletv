package server

import (
	"context"
	"sync"
	"time"

	"github.com/palantir/stacktrace"
	"github.com/tnyim/jungletv/proto"
	"github.com/tnyim/jungletv/server/auth"
	"github.com/tnyim/jungletv/utils/event"
)

func (s *grpcServer) ConsumeMedia(r *proto.ConsumeMediaRequest, stream proto.JungleTV_ConsumeMediaServer) error {
	// stream.Send is not safe to be called on concurrent goroutines
	streamSendLock := sync.Mutex{}
	var initialActivityChallenge *activityChallenge
	send := func(cp *proto.MediaConsumptionCheckpoint) error {
		streamSendLock.Lock()
		defer streamSendLock.Unlock()
		if initialActivityChallenge != nil {
			cp.ActivityChallenge = initialActivityChallenge.SerializeForAPI()
			initialActivityChallenge = nil
		}
		return stream.Send(cp)
	}

	counter, err := s.getAnnouncementsCounter(stream.Context())
	if err != nil {
		return stacktrace.Propagate(err, "")
	}

	user := auth.UserClaimsFromContext(stream.Context())
	initialCp := s.produceMediaConsumptionCheckpoint(stream.Context())
	v := uint32(counter.CounterValue)
	initialCp.LatestAnnouncement = &v
	err = stream.Send(initialCp)
	if err != nil {
		return stacktrace.Propagate(err, "")
	}

	errChan := make(chan error)

	if user != nil {
		spectator, err := s.rewardsHandler.RegisterSpectator(stream.Context(), user)
		if err != nil {
			return stacktrace.Propagate(err, "")
		}

		// SubscribeUsingCallback returns a function that unsubscribes when called. That's the reason for the defers

		defer spectator.OnRewarded().SubscribeUsingCallback(event.AtLeastOnceGuarantee, func(reward Amount, rewardBalance Amount) {
			cp := s.produceMediaConsumptionCheckpoint(stream.Context())
			s := reward.String()
			cp.Reward = &s
			s2 := rewardBalance.String()
			cp.RewardBalance = &s2
			err := send(cp)
			if err != nil {
				errChan <- stacktrace.Propagate(err, "")
			}
		})()

		defer spectator.OnWithdrew().SubscribeUsingCallback(event.AtLeastOnceGuarantee, func() {
			cp := s.produceMediaConsumptionCheckpoint(stream.Context())
			s2 := "0"
			cp.RewardBalance = &s2
			err := send(cp)
			if err != nil {
				errChan <- stacktrace.Propagate(err, "")
			}
		})()

		initialActivityChallenge = spectator.CurrentActivityChallenge()
		defer spectator.OnActivityChallenge().SubscribeUsingCallback(event.AtLeastOnceGuarantee, func(challenge *activityChallenge) {
			cp := s.produceMediaConsumptionCheckpoint(stream.Context())
			cp.ActivityChallenge = challenge.SerializeForAPI()
			err := send(cp)
			if err != nil {
				errChan <- stacktrace.Propagate(err, "")
			}
		})()

		defer spectator.OnChatMentioned().SubscribeUsingCallback(event.AtLeastOnceGuarantee, func() {
			cp := s.produceMediaConsumptionCheckpoint(stream.Context())
			t := true
			cp.HasChatMention = &t
			err := send(cp)
			if err != nil {
				errChan <- stacktrace.Propagate(err, "")
			}
		})()

		defer s.rewardsHandler.UnregisterSpectator(stream.Context(), spectator)
	}

	defer s.announcementsUpdated.SubscribeUsingCallback(event.AtLeastOnceGuarantee, func(counterValue int) {
		cp := s.produceMediaConsumptionCheckpoint(stream.Context())
		v := uint32(counterValue)
		cp.LatestAnnouncement = &v
		err := send(cp)
		if err != nil {
			errChan <- stacktrace.Propagate(err, "")
		}
	})()

	statsCleanup, err := s.statsHandler.RegisterSpectator(stream.Context())
	if err != nil {
		return stacktrace.Propagate(err, "")
	}
	defer statsCleanup()

	t := time.NewTicker(3 * time.Second)
	// if we set this ticker to e.g. 10 seconds, it seems to be too long and CloudFlare or something drops connection :(

	onMediaChanged, mediaChangedU := s.mediaQueue.mediaChanged.Subscribe(event.AtLeastOnceGuarantee)
	defer mediaChangedU()
	for {
		select {
		case <-t.C:
			break
		case <-onMediaChanged:
			break
		case <-stream.Context().Done():
			return nil
		case err := <-errChan:
			return err
		}
		err := send(s.produceMediaConsumptionCheckpoint(stream.Context()))
		if err != nil {
			return stacktrace.Propagate(err, "")
		}
	}
}

func (s *grpcServer) produceMediaConsumptionCheckpoint(ctx context.Context) *proto.MediaConsumptionCheckpoint {
	cp := s.mediaQueue.ProduceCheckpointForAPI(ctx, s.userSerializer)
	cp.CurrentlyWatching = uint32(s.statsHandler.CurrentlyWatching())
	return cp
}
