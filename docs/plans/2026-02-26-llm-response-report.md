# LLM Response PDF Report Implementation Plan

> **For Claude:** Execute directly on main - this is a small change not requiring a worktree.

**Goal:** Add `report_type: "llm_response"` to the PDF reports tool so the LLM can pass Markdown content and get a branded PDF.

**Architecture:** A new renderer in `pdf_reports/main.py` that converts Markdown to HTML using python-markdown, then renders via a new template that reuses the corporate header/footer from formal_report.

**Tech Stack:** python-markdown, Jinja2, WeasyPrint

---

### Task 1: Add markdown dependency

**File:** `tools/pdf_reports/requirements.txt`

Add `markdown>=3.5` to the file.

---

### Task 2: Add render_llm_response function

**File:** `tools/pdf_reports/main.py`

**Step 1: Import markdown**

Add after the weasyprint import (line ~33):
```python
import markdown
```

**Step 2: Add render_llm_response function**

Add before `main()` (around line 250):
```python
def render_llm_response(data: dict[str, Any], env: Environment) -> str:
    """Render LLM response as PDF report with corporate styling."""
    template = env.get_template("llm_response.html")

    content_markdown = data.get("content", "")
    content_html = markdown.markdown(
        content_markdown,
        extensions=["tables", "fenced_code", "nl2br"]
    )

    context = build_base_context(data, "llm_response")
    context.update({
        "content_html": content_html,
        "author": data.get("author", "AI Assistant"),
        "logo_url": data.get("logo_url"),
        "confidentiality": data.get("confidentiality", "Internal Document"),
    })

    return template.render(**context)
```

**Step 3: Register in renderers dict**

In `main()`, add to the `renderers` dict (around line 296):
```python
"llm_response": render_llm_response,
```

---

### Task 3: Create llm_response.html template

**File:** `templates/reports/llm_response.html`

```html
<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <title>{{ title }}</title>
    <link rel="stylesheet" href="styles.css">
</head>
<body>
    {% if logo_url %}
    <div class="logo-container">
        <img src="{{ logo_url }}" alt="Company Logo">
    </div>
    {% endif %}

    <div class="report-header">
        <div class="report-type">AI Generated Report</div>
        <h1>{{ title }}</h1>
        <div class="report-date">Generated: {{ generated_at }}</div>
        {% if author %}
        <div class="report-author">Author: {{ author }}</div>
        {% endif %}
    </div>

    <div class="llm-content">
        {{ content_html | safe }}
    </div>

    <footer>
        <div class="confidential">{{ confidentiality }}</div>
        <p class="generated-at">Report generated on {{ generated_at }}</p>
    </footer>
</body>
</html>
```

---

### Task 4: Add CSS styles for Markdown

**File:** `templates/reports/styles.css`

Add at the end (before the closing brace if any):

```css
/* LLM Response Content Styles */
.llm-content {
    font-family: 'Helvetica Neue', Arial, sans-serif;
    font-size: 11pt;
    line-height: 1.6;
    color: #1a1a1a;
}

.llm-content h1 {
    font-size: 18pt;
    margin-top: 25px;
    margin-bottom: 15px;
    border-bottom: 1px solid #ced4da;
    padding-bottom: 8px;
}

.llm-content h2 {
    font-size: 15pt;
    margin-top: 20px;
    margin-bottom: 12px;
}

.llm-content h3 {
    font-size: 13pt;
    margin-top: 15px;
    margin-bottom: 8px;
}

.llm-content p {
    margin-bottom: 12px;
    text-align: justify;
}

.llm-content ul, .llm-content ol {
    margin: 10px 0 15px 25px;
}

.llm-content li {
    margin-bottom: 6px;
}

.llm-content pre {
    background-color: #f5f5f5;
    border: 1px solid #dee2e6;
    border-radius: 4px;
    padding: 12px 15px;
    overflow-x: auto;
    font-family: 'Courier New', monospace;
    font-size: 10pt;
    margin: 15px 0;
}

.llm-content code {
    font-family: 'Courier New', monospace;
    font-size: 10pt;
    background-color: #f5f5f5;
    padding: 2px 5px;
    border-radius: 3px;
}

.llm-content pre code {
    background: none;
    padding: 0;
}

.llm-content blockquote {
    border-left: 4px solid #0d1b2a;
    margin: 15px 0;
    padding: 10px 20px;
    background-color: #f8f9fa;
    font-style: italic;
}

.llm-content table {
    width: 100%;
    border-collapse: collapse;
    margin: 15px 0;
}

.llm-content th, .llm-content td {
    padding: 8px 12px;
    border: 1px solid #dee2e6;
    text-align: left;
}

.llm-content th {
    background-color: #0d1b2a;
    color: #ffffff;
    font-weight: 600;
}

.llm-content img {
    max-width: 100%;
    height: auto;
    margin: 15px 0;
}
```

---

### Task 5: Build and verify

```bash
cd /home/hp/Proyectos/mcp-go
go build -o bin/mcp-server ./cmd/server && go vet ./...
```

---

### Task 6: Commit

```bash
git add tools/pdf_reports/requirements.txt tools/pdf_reports/main.py templates/reports/llm_response.html templates/reports/styles.css
git commit -m "feat(pdf_reports): add llm_response report type for Markdown content"
```
