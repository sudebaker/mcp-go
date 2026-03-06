#!/usr/bin/env python3
"""
RSS Reader Tool for MCP Orchestrator.
Reads RSS/Atom feeds and returns news items.
"""

import json
import sys
import os
import re
import time
import random
import traceback
from datetime import datetime
from typing import Any, Optional
from urllib.parse import urlparse

sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))

try:
    import feedparser
    FEEDPARSER_AVAILABLE = True
except ImportError:
    FEEDPARSER_AVAILABLE = False

try:
    import requests
    REQUESTS_AVAILABLE = True
except ImportError:
    REQUESTS_AVAILABLE = False


DEFAULT_LIMIT = 10
FEED_TIMEOUT = 10
MIN_REQUEST_INTERVAL = 1
MAX_REQUEST_INTERVAL = 5

USER_AGENTS = [
    "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36",
    "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36",
    "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:121.0) Gecko/20100101 Firefox/121.0",
    "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.2 Safari/605.1.15",
    "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36",
    "Mozilla/5.0 (X11; Linux x86_64; rv:121.0) Gecko/20100101 Firefox/121.0",
]

RATE_LIMIT_FILE = "/tmp/mcp_rss_reader_rate_limit.json"
FEEDS_FILE = os.path.join(os.path.dirname(__file__), "feeds.json")

_last_request_time: dict[str, float] = {}

INTERNAL_IP_PATTERNS = [
    r"^127\.",
    r"^10\.",
    r"^172\.(1[6-9]|2\d|3[01])\.",
    r"^192\.168\.",
    r"^169\.254\.",
    r"^0\.",
    r"^localhost$",
    r"^::1$",
]

BLOCKED_HOSTS = [
    "169.254.169.254",
    "metadata.google.internal",
    "metadata.googleusercontent.com",
    "instance-data",
]

compiled_ip_patterns = [re.compile(p) for p in INTERNAL_IP_PATTERNS]


def is_internal_url(url: str) -> bool:
    try:
        parsed = urlparse(url)
        host = parsed.hostname or ""
        
        if host.lower() in BLOCKED_HOSTS:
            return True

        if host.lower() == "localhost" or host == "::1":
            return True

        for pattern in compiled_ip_patterns:
            if pattern.match(host):
                return True

        return False
    except Exception:
        return True


def _load_rate_limit_state() -> dict[str, float]:
    try:
        if os.path.exists(RATE_LIMIT_FILE):
            with open(RATE_LIMIT_FILE, "r") as f:
                return json.load(f)
    except Exception:
        pass
    return {}


def _save_rate_limit_state(state: dict[str, float]) -> None:
    try:
        with open(RATE_LIMIT_FILE, "w") as f:
            json.dump(state, f)
    except Exception:
        pass


def read_request() -> dict[str, Any]:
    input_data = sys.stdin.read()
    return json.loads(input_data)


def write_response(response: dict[str, Any]) -> None:
    print(json.dumps(response, default=str))


def load_feeds() -> list[dict[str, str]]:
    try:
        with open(FEEDS_FILE, "r") as f:
            data = json.load(f)
            return data.get("feeds", [])
    except Exception as e:
        return []


def _apply_rate_limit(host: str) -> None:
    global _last_request_time
    _last_request_time = _load_rate_limit_state()
    
    current_time = time.time()
    last_time = _last_request_time.get(host, 0)
    
    if current_time - last_time < MIN_REQUEST_INTERVAL:
        delay = random.uniform(MIN_REQUEST_INTERVAL, MAX_REQUEST_INTERVAL)
        time.sleep(delay)
        current_time = time.time()
    
    _last_request_time[host] = current_time
    _save_rate_limit_state(_last_request_time)


def fetch_feed(feed_url: str, feed_name: str) -> tuple[Optional[list], Optional[str]]:
    if not FEEDPARSER_AVAILABLE:
        return None, "feedparser not installed. Install with: pip install feedparser"

    try:
        parsed_url = urlparse(feed_url)
        host = parsed_url.hostname or ""
        
        if is_internal_url(feed_url):
            return None, "Access to internal URLs is not allowed"
        
        _apply_rate_limit(host)

        headers = {
            "User-Agent": random.choice(USER_AGENTS),
            "Accept": "application/rss+xml, application/atom+xml, application/xml, text/xml, */*",
            "Accept-Language": "en-US,en;q=0.5",
        }

        if REQUESTS_AVAILABLE:
            response = requests.get(feed_url, headers=headers, timeout=FEED_TIMEOUT)
            response.raise_for_status()
            feed_content = response.content
        else:
            import urllib.request
            req = urllib.request.Request(feed_url, headers=headers)
            with urllib.request.urlopen(req, timeout=FEED_TIMEOUT) as response:
                feed_content = response.read()

        feed = feedparser.parse(feed_content)
        
        if feed.bozo and not feed.entries:
            return None, f"Failed to parse feed: {feed.bozo_exception}"

        items = []
        for entry in feed.entries[:20]:
            item = {
                "title": _clean_text(entry.get("title", "")),
                "link": entry.get("link", ""),
                "published": _format_date(entry.get("published", entry.get("updated", ""))),
                "source": feed_name,
            }
            
            if hasattr(entry, "content") and entry.content:
                item["content"] = _clean_text(entry.content[0].value)
            elif hasattr(entry, "summary"):
                item["content"] = _clean_text(entry.summary)
            elif hasattr(entry, "description"):
                item["content"] = _clean_text(entry.description)
            
            items.append(item)

        return items, None

    except Exception as e:
        return None, f"Failed to fetch feed: {str(e)}"


def _clean_text(text: str) -> str:
    if not text:
        return ""
    text = re.sub(r"<[^>]+>", "", text)
    text = re.sub(r"\s+", " ", text)
    return text.strip()


def _format_date(date_str: str) -> str:
    if not date_str:
        return ""
    try:
        parsed = feedparser.parse_date(date_str)
        dt = datetime(*parsed[:6])
        return dt.strftime("%Y-%m-%d %H:%M")
    except Exception:
        return date_str[:16] if len(date_str) > 16 else date_str


def filter_feeds(feeds: list[dict], selected_names: Optional[list]) -> list[dict]:
    if not selected_names:
        return feeds
    selected_lower = [name.lower() for name in selected_names]
    return [f for f in feeds if f["name"].lower() in selected_lower]


def main() -> None:
    request = {}
    try:
        request = read_request()
        request_id = request.get("request_id", "")
        arguments = request.get("arguments", {})
        context = request.get("context", {})

        limit = arguments.get("limit", DEFAULT_LIMIT)
        selected_feeds = arguments.get("feeds", None)
        extract_type = arguments.get("extract", "titles")

        if limit < 1:
            limit = 1
        if limit > 50:
            limit = 50

        all_feeds = load_feeds()
        if not all_feeds:
            write_response({
                "success": False,
                "request_id": request_id,
                "error": {
                    "code": "NO_FEEDS",
                    "message": "No feeds configured or feeds.json not found"
                }
            })
            return

        feeds_to_fetch = filter_feeds(all_feeds, selected_feeds)
        
        if not feeds_to_fetch:
            write_response({
                "success": False,
                "request_id": request_id,
                "error": {
                    "code": "INVALID_FEEDS",
                    "message": f"No feeds matched: {selected_feeds}"
                }
            })
            return

        all_items = []
        errors = []
        feeds_queried = []

        for feed in feeds_to_fetch:
            feed_name = feed["name"]
            feed_url = feed["url"]
            feeds_queried.append(feed_name)
            
            items, error = fetch_feed(feed_url, feed_name)
            
            if error:
                errors.append(f"{feed_name}: {error}")
            elif items:
                all_items.extend(items[:limit])

        all_items = all_items[:limit * len(feeds_to_fetch)]

        all_items.sort(key=lambda x: x["published"], reverse=True)
        all_items = all_items[:limit * 3]

        if extract_type == "titles":
            display_items = [{"title": i["title"], "link": i["link"], "published": i["published"], "source": i["source"]} for i in all_items]
        else:
            display_items = all_items

        summary_text = f"**Feeds queried:** {', '.join(feeds_queried)}\n\n"
        summary_text += f"**Total items:** {len(all_items)}\n\n"
        
        if errors:
            summary_text += f"**Errors:** {'; '.join(errors[:3])}\n\n"
        
        summary_text += "**Headlines:**\n"
        for i, item in enumerate(display_items[:20], 1):
            summary_text += f"{i}. [{item['title']}]({item['link']}) ({item['source']})\n"

        write_response({
            "success": True,
            "request_id": request_id,
            "content": [
                {
                    "type": "text",
                    "text": summary_text
                }
            ],
            "structured_content": {
                "items": all_items,
                "total_items": len(all_items),
                "feeds_queried": feeds_queried,
                "errors": errors if errors else None,
                "limit_used": limit
            }
        })

    except json.JSONDecodeError as e:
        write_response({
            "success": False,
            "request_id": request.get("request_id", ""),
            "error": {
                "code": "INVALID_INPUT",
                "message": f"Failed to parse JSON input: {str(e)}"
            }
        })
    except Exception as e:
        write_response({
            "success": False,
            "request_id": request.get("request_id", "") if request else "",
            "error": {
                "code": "EXECUTION_FAILED",
                "message": str(e),
                "details": traceback.format_exc()
            }
        })


if __name__ == "__main__":
    main()
