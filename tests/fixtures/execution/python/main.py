import asyncio

def leaf(n):
    return n * 2

async def run(n, callback=leaf):
    if n < 0:
        raise ValueError("negative input")
    await asyncio.sleep(0)
    return callback(n)

def values(n):
    for value in range(n):
        yield leaf(value)

if __name__ == "__main__":
    print(asyncio.run(run(1)))
