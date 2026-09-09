import { useCallback, useEffect, useState } from "react";
import { ApiRequestError } from "./api";

interface AsyncState<T> {
  data: T | null;
  loading: boolean;
  error: string | null;
  reload: () => void;
}

function message(e: unknown): string {
  if (e instanceof ApiRequestError) {
    return e.info.status === 403
      ? "Your account doesn't have an operator role for this."
      : e.info.reason || e.info.code;
  }
  return e instanceof Error ? e.message : String(e);
}

// useAsync runs `fn` on mount and whenever `key` changes (pass a string built
// from the inputs, e.g. the current filter), and exposes a manual reload().
// `fn` is deliberately not a dependency — `key` stands in for its inputs.
export function useAsync<T>(fn: () => Promise<T>, key: string): AsyncState<T> {
  const [data, setData] = useState<T | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [tick, setTick] = useState(0);

  useEffect(() => {
    let cancelled = false;
    setLoading(true);
    setError(null);
    fn()
      .then((d) => !cancelled && setData(d))
      .catch((e) => !cancelled && setError(message(e)))
      .finally(() => !cancelled && setLoading(false));
    return () => {
      cancelled = true;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [key, tick]);

  const reload = useCallback(() => setTick((t) => t + 1), []);
  return { data, loading, error, reload };
}
