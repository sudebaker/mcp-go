import re

MAX_CONTENT_LENGTH = 50000
UNTRUSTED_START = "[EXTERNAL_UNTRUSTED_CONTENT]\n"
UNTRUSTED_END = "\n[/EXTERNAL_UNTRUSTED_CONTENT]"

INJECTION_PATTERNS = [
    re.compile(r'ignore\s*previous\s*instructions?', re.IGNORECASE),
    re.compile(r'ignora\s*tus\s*instrucciones', re.IGNORECASE),
    re.compile(r'you\s*are\s*now\s*', re.IGNORECASE),
    re.compile(r'ahora\s*eres', re.IGNORECASE),
    re.compile(r'<<?\s*system\s*>?', re.IGNORECASE),
    re.compile(r'<<[^>]+>>', re.IGNORECASE),
    re.compile(r'\[\s*INST\s*\]', re.IGNORECASE),
    re.compile(r'<\|[^|]*\|>', re.IGNORECASE),
    re.compile(r'```system', re.IGNORECASE),
    re.compile(r'user:', re.IGNORECASE),
    re.compile(r'assistant:', re.IGNORECASE),
    re.compile(r'human:', re.IGNORECASE),
    re.compile(r'ai:', re.IGNORECASE),
]

def _strip_injection_patterns(text: str) -> str:
    for pattern in INJECTION_PATTERNS:
        text = pattern.sub('', text)
    return text

def _normalize_whitespace(text: str) -> str:
    text = re.sub(r'\n{3,}', '\n\n', text)
    text = re.sub(r' {2,}', ' ', text)
    return text.strip()

def sanitize_external_content(text: str) -> str:
    if not text:
        return UNTRUSTED_START + UNTRUSTED_END

    if len(text) > MAX_CONTENT_LENGTH:
        text = text[:MAX_CONTENT_LENGTH]

    text = _strip_injection_patterns(text)
    text = _normalize_whitespace(text)

    return UNTRUSTED_START + text + UNTRUSTED_END
