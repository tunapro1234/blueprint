"""Read-only consumer of bp status schema 2; no model, messages, or state writes."""
import datetime as dt
import json
from pathlib import Path

MAX_BYTES = 40 * 1024 * 1024
FIELDS = ('input_tokens', 'output_tokens', 'cache_creation_input_tokens', 'cache_read_input_tokens')


def timestamp(value):
    try:
        parsed = dt.datetime.fromisoformat(value.replace('Z', '+00:00'))
        return parsed if parsed.tzinfo else None
    except (ValueError, TypeError, AttributeError):
        return None


def measure(path, harness, since, now):
    result = dict(tokens=0, weighted_units=0.0, messages=0, goal_6h=0,
                  complete=True, scope='event timestamps in window', last_usage_at=None)
    messages, previous = {}, None
    try:
        with Path(path).open('rb') as f:
            size = f.seek(0, 2)
            offset = max(0, size - MAX_BYTES)
            f.seek(offset)
            if offset:
                f.readline()
                result['complete'] = False
            for line in f:
                if not line.endswith(b'\n'):
                    result['complete'] = False
                    continue
                try:
                    row = json.loads(line)
                except (ValueError, UnicodeError):
                    result['complete'] = False
                    continue
                when = timestamp(row.get('timestamp'))
                if when is None or when > now:
                    continue
                if harness.startswith('codex'):
                    payload = row.get('payload') or {}
                    if row.get('type') != 'event_msg' or payload.get('type') != 'token_count':
                        continue
                    info = payload.get('info') or {}
                    total = (info.get('total_token_usage') or {}).get('total_tokens')
                    if not isinstance(total, int) or total < 0:
                        continue
                    if when >= since:
                        if previous is None or total < previous:
                            # Missing baseline/reset is not a newly consumed total.
                            result['complete'] = False
                        else:
                            result['tokens'] += total - previous
                            result['last_usage_at'] = when.isoformat()
                    previous = total
                    continue
                if row.get('isSidechain'):
                    continue
                if (row.get('attachment') or {}).get('type') == 'goal_status' and when >= now - dt.timedelta(hours=6):
                    result['goal_6h'] += 1
                if when < since or row.get('type') != 'assistant':
                    continue
                msg = row.get('message') or {}
                usage = msg.get('usage')
                if not isinstance(usage, dict):
                    continue
                key = msg.get('id') or row.get('uuid')
                if not key:
                    result['complete'] = False
                    continue
                prior = messages.setdefault(key, dict.fromkeys(FIELDS, 0))
                for field in FIELDS:
                    value = usage.get(field, 0)
                    if isinstance(value, (int, float)) and value >= 0:
                        prior[field] = max(prior[field], value)
                result['last_usage_at'] = when.isoformat()
    except OSError:
        result['complete'] = False
    for usage in messages.values():
        result['tokens'] += sum(usage.values())
        result['weighted_units'] += (usage['input_tokens'] + usage['cache_creation_input_tokens'] * 1.25
                                     + usage['cache_read_input_tokens'] * .1 + usage['output_tokens'] * 5)
    result['messages'] = len(messages)
    return result


def inspect(report, now, hours=12):
    if report.get('schema_version') != 2:
        raise ValueError('bp runtime schema 2 required; live bp has not been upgraded')
    since = now - dt.timedelta(hours=hours)
    results = []
    for agent in report.get('agents', []):
        activity = agent.get('activity') or {}
        observed = timestamp(activity.get('observed_at'))
        fresh = observed is not None and 0 <= (now - observed).total_seconds() <= 30
        path, thread = activity.get('transcript_path'), activity.get('thread_id')
        bound = bool(path and thread and activity.get('binding'))
        harness = agent.get('runtime', '')
        current = activity.get('state', 'unknown') if fresh else 'unknown'
        item = dict(agent=agent['name'], thread_id=thread, runtime_state=current,
                    last_human_age_seconds=agent.get('last_human_age_seconds'),
                    window_start=since.isoformat(), window_end=now.isoformat(),
                    running=current == 'working', can_alert=False)
        if bound and harness in ('claude', 'codex', 'codex-remote'):
            human_age = agent.get('last_human_age_seconds')
            usage_since = since
            if isinstance(human_age, (int, float)) and human_age >= 0:
                usage_since = max(since, now - dt.timedelta(seconds=human_age))
            item['usage_since'] = usage_since.isoformat()
            item['usage'] = measure(path, harness, usage_since, now)
            item['can_alert'] = item['running'] and item['usage']['complete'] and item['usage']['tokens'] > 0
        results.append(item)
    return results
