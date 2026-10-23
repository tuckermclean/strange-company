import os


def test_env():
    assert os.environ.get("PYTEST_CURRENT_TEST")
