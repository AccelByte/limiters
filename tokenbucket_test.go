package limiters_test

import (
	"context"
	"sync"
	"time"

	"github.com/google/uuid"

	l "github.com/mennanov/limiters"
)

var tokenBucketUniformTestCases = []struct {
	capacity     int64
	refillRate   time.Duration
	requestCount int64
	requestRate  time.Duration
	missExpected int
	delta        float64
}{
	{
		5,
		time.Millisecond * 100,
		10,
		time.Millisecond * 25,
		3,
		1,
	},
	{
		10,
		time.Millisecond * 100,
		13,
		time.Millisecond * 25,
		0,
		1,
	},
	{
		10,
		time.Millisecond * 100,
		15,
		time.Millisecond * 33,
		2,
		1,
	},
	{
		10,
		time.Millisecond * 100,
		16,
		time.Millisecond * 33,
		2,
		1,
	},
	{
		10,
		time.Millisecond * 10,
		20,
		time.Millisecond * 10,
		0,
		1,
	},
	{
		1,
		time.Millisecond * 20,
		20,
		time.Millisecond * 10,
		10,
		2,
	},
}

// tokenBuckets returns all the possible TokenBucket combinations.
func (s *LimitersTestSuite) tokenBuckets(capacity int64, refillRate time.Duration, clock l.Clock) []*l.TokenBucket {
	return []*l.TokenBucket{
		l.NewTokenBucket(l.NewLockerNoop(), l.NewTokenBucketInMemory(l.TokenBucketState{
			RefillRate: refillRate,
			Capacity:   capacity,
			Available:  capacity,
		}), clock, s.logger),
		l.NewTokenBucket(
			l.NewLockerEtcd(s.etcdClient, uuid.New().String(), s.logger),
			l.NewTokenBucketEtcd(s.etcdClient, uuid.New().String(), l.TokenBucketState{
				RefillRate: refillRate,
				Capacity:   capacity,
				Available:  capacity,
			}, time.Second), clock, s.logger),
		l.NewTokenBucket(
			l.NewLockerEtcd(s.etcdClient, uuid.New().String(), s.logger),
			l.NewTokenBucketRedis(s.redisClient, uuid.New().String(), l.TokenBucketState{
				RefillRate: refillRate,
				Capacity:   capacity,
				Available:  capacity,
			}, time.Second), clock, s.logger),
	}
}

func (s *LimitersTestSuite) TestTokenBucketRealClock() {
	clock := l.NewSystemClock()
	for _, testCase := range tokenBucketUniformTestCases {
		for _, bucket := range s.tokenBuckets(testCase.capacity, testCase.refillRate, clock) {
			wg := sync.WaitGroup{}
			// mu guards the miss variable below.
			var mu sync.Mutex
			miss := 0
			for i := int64(0); i < testCase.requestCount; i++ {
				// No pause for the first request.
				if i > 0 {
					clock.Sleep(testCase.requestRate)
				}
				wg.Add(1)
				go func() {
					defer wg.Done()
					if _, err := bucket.Limit(context.TODO()); err != nil {
						s.Equal(l.ErrLimitExhausted, err)
						mu.Lock()
						miss++
						mu.Unlock()
					}
				}()
			}
			wg.Wait()
			s.InDelta(testCase.missExpected, miss, testCase.delta, testCase)
		}
	}
}

func (s *LimitersTestSuite) TestTokenBucketFakeClock() {
	for _, testCase := range tokenBucketUniformTestCases {
		clock := newFakeClock()
		for _, bucket := range s.tokenBuckets(testCase.capacity, testCase.refillRate, clock) {
			clock.reset()
			miss := 0
			for i := int64(0); i < testCase.requestCount; i++ {
				// No pause for the first request.
				if i > 0 {
					clock.Sleep(testCase.requestRate)
				}
				if _, err := bucket.Limit(context.TODO()); err != nil {
					s.Equal(l.ErrLimitExhausted, err)
					miss++
				}
			}
			s.InDelta(testCase.missExpected, miss, testCase.delta, testCase)
		}
	}
}

func (s *LimitersTestSuite) TestTokenBucketOverflow() {
	clock := newFakeClock()
	rate := time.Second
	for _, bucket := range s.tokenBuckets(2, rate, clock) {
		clock.reset()
		wait, err := bucket.Limit(context.TODO())
		s.Require().NoError(err)
		s.Equal(time.Duration(0), wait)
		wait, err = bucket.Limit(context.TODO())
		s.Require().NoError(err)
		s.Equal(time.Duration(0), wait)
		// The third call should fail.
		wait, err = bucket.Limit(context.TODO())
		s.Require().Equal(l.ErrLimitExhausted, err)
		s.Equal(rate, wait)
		clock.Sleep(wait)
		// Retry the last call.
		wait, err = bucket.Limit(context.TODO())
		s.Require().NoError(err)
		s.Equal(time.Duration(0), wait)
	}
}

func (s *LimitersTestSuite) TestTokenBucketContextCancelled() {
	clock := newFakeClock()
	for _, bucket := range s.tokenBuckets(1, 1, clock) {
		done1 := make(chan struct{})
		go func() {
			defer close(done1)
			// The context is expired shortly after it is created.
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			_, err := bucket.Limit(ctx)
			s.Error(err, bucket)
		}()
		done2 := make(chan struct{})
		go func() {
			defer close(done2)
			<-done1
			ctx := context.Background()
			_, err := bucket.Limit(ctx)
			s.NoError(err)
		}()
		// Verify that the second go routine succeeded calling the Limit() method.
		<-done2
	}
}