"""Command line interface:  gonexus-client URI [KEY [PATH]]  (or  python -m gonexus_client ...)."""
import sys
from typing import List, Optional

import numpy as np

from .client import Field, from_uri


def main(argv: Optional[List[str]] = None) -> int:
    argv = list(sys.argv if argv is None else argv)
    if len(argv) < 2:
        print("usage: gonexus-client URI [KEY [PATH]]\n"
              "  URI            list files\n  URI KEY        print file tree\n"
              "  URI KEY PATH   describe a group/field (fields: print a data summary)")
        return 2
    c = from_uri(argv[1])
    if len(argv) == 2:
        for k, info in c._items().items():
            print(f"{k}\t{info.get('size', '')}")
        return 0
    f = c[argv[2]] if argv[2] in c._items() else c.open(argv[2])
    if len(argv) == 3:
        print(f.tree())
        return 0
    node = f[argv[3]]
    print(node)
    for k, v in node.attrs.items():
        print(f"  @{k} = {v}")
    if isinstance(node, Field):
        a = np.asarray(node[0] if node.ndim > 1 else node.read())
        print("first row/value:", a if a.size <= 10 else f"{a.ravel()[:5]} ... ({a.size} values)")
    else:
        print("children:", list(node))
    return 0


if __name__ == "__main__":
    sys.exit(main())
