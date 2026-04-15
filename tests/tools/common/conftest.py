#!/usr/bin/env python3
"""
Pytest configuration for tests directory.
Sets up Python path to include tools and tools/common directories.
"""

import os
import sys

sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "tools"))
sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "tools", "common"))
