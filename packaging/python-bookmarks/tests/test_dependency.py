from shinyhub_bookmarks import bookmarking_dependency


def test_dependency_serves_the_packaged_bridge():
    dependency = bookmarking_dependency()
    assert dependency.name == "shinyhub-bookmarks"
    assert dependency.source == {"package": "shinyhub_bookmarks", "subdir": "www"}
    assert dependency.script == [{"src": "bridge.js", "defer": "defer"}]

