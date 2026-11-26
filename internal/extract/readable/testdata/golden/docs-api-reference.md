Every API key may make up to 600 requests per minute, counted in a rolling window, and requests above that limit are refused with status 429 until the window has room again.

Each response carries three headers that tell you where you stand, so a well-behaved client never needs to guess. Read them on every response rather than keeping your own count, because requests from other processes sharing the key count against the same window.

| Header                  | Meaning                                                |
|-------------------------|--------------------------------------------------------|
| `X-RateLimit-Limit`     | Requests allowed in the current window.                |
| `X-RateLimit-Remaining` | Requests left before the limit is reached.             |
| `Retry-After`           | Seconds to wait before retrying, sent only with a 429. |

## Handling a 429

When you receive a 429, wait for the number of seconds in `Retry-After` before sending the request again. Retrying sooner does not help: the request is refused and still counts towards the next window, which only prolongs the wait for every process using the key.

```
if resp.StatusCode == 429 {
    wait, _ := strconv.Atoi(resp.Header.Get("Retry-After"))
    time.Sleep(time.Duration(wait) * time.Second)
}
```

## Higher limits

Accounts on the Business plan can ask for a higher limit for a single key. Write to support with the key's identifier and the traffic you expect at peak, and we will usually reply within one working day.