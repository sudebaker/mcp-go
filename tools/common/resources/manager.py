from .resource import Resource


class ToolContext:
    def __init__(self, request: dict):
        self._request = request
        self._resources = request.get("_resources", {})

    def file(self, arg_name: str) -> Resource:
        meta = self._resources[arg_name]
        return Resource(
            uri=meta["uri"],
            name=meta["name"],
            mime=meta["mime"],
            size=meta["size"],
            sha256=meta["sha256"],
        )

    def files(self, arg_name: str) -> list[Resource]:
        metas = self._resources[arg_name]
        return [
            Resource(
                uri=m["uri"],
                name=m["name"],
                mime=m["mime"],
                size=m["size"],
                sha256=m["sha256"],
            )
            for m in metas
        ]

    @property
    def request(self) -> dict:
        return self._request
