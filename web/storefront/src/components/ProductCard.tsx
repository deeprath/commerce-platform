import { mediaUrl } from "../api";
import { formatMoney } from "../types";
import type { Hit } from "../types";

export function ProductCard({ hit }: { hit: Hit }) {
  return (
    <article className="card">
      <div className="card-img">
        {hit.primary_media_key ? (
          <img src={mediaUrl(hit.primary_media_key)} alt={hit.title} loading="lazy" />
        ) : (
          <div className="noimg" aria-hidden />
        )}
      </div>
      <h2>{hit.title}</h2>
      <p className="price">{formatMoney(hit.list_price)}</p>
    </article>
  );
}
