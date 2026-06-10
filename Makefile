# MCP-Go Development Commands

# Type check Python tools
typecheck:
	@echo "Running mypy type checking..."
	@source venv/bin/activate && python -m mypy tools/common tools/echo/main.py tools/datetime_tool/main.py tools/weather/main.py

typecheck-strict:
	@echo "Running mypy type checking with strict mode..."
	@source venv/bin/activate && python -m mypy --strict tools/common tools/echo/main.py tools/datetime_tool/main.py tools/weather/main.py

# Install Python dependencies
install-deps:
	pip install -r requirements.txt

# Format code
format:
	@echo "Formatting code..."
	black tools/common tools/echo/main.py tools/datetime_tool/main.py tools/weather/main.py
	isort tools/common tools/echo/main.py tools/datetime_tool/main.py tools/weather/main.py

# Lint code
lint:
	@echo "Linting code..."
	flake8 tools/common tools/echo/main.py tools/datetime_tool/main.py tools/weather/main.py
	pylint tools/common tools/echo/main.py tools/datetime_tool/main.py tools/weather/main.py

.PHONY: typecheck typecheck-strict install-deps format lint