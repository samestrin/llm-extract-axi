Retries look like a free reliability win, but a naive retry loop turns a brief outage into a long one by multiplying the load on a server that is already struggling.

This post walks through the three rules I now apply to every retry loop I write: retry only what is safe to repeat, back off exponentially with jitter, and give up after a budget rather than a count.

## Only retry what is safe

A GET is safe to repeat. A POST that charges a card is not, unless the server accepts an idempotency key. If you cannot tell whether a request is safe, do not retry it automatically; surface the error and let a person decide.

## Back off with jitter

If a thousand clients all retry after exactly one second, the server sees a thousand requests at once, every second. Adding random jitter spreads those retries out so the server can recover.

```
func backoff(attempt int) time.Duration {
	base := 100 * time.Millisecond << attempt
	return base/2 + time.Duration(rand.Int63n(int64(base/2)))
}
```

## Spend a budget, not a count

Five retries with exponential backoff can take minutes. Give the whole operation a deadline with a context, and stop retrying when the deadline would pass before the next attempt finishes.