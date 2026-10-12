package io.github.seventytwo.kitchensink

import android.app.Application
import coil3.ImageLoader
import coil3.PlatformContext
import coil3.SingletonImageLoader
import coil3.network.okhttp.OkHttpNetworkFetcherFactory
import io.github.seventytwo.kitchensink.data.ConfigStore
import java.util.concurrent.TimeUnit
import okhttp3.OkHttpClient

class KitchensinkApp : Application(), SingletonImageLoader.Factory {
    val http: OkHttpClient by lazy {
        OkHttpClient.Builder()
            .connectTimeout(15, TimeUnit.SECONDS)
            .readTimeout(30, TimeUnit.SECONDS)
            .build()
    }

    val hub: Hub by lazy { Hub(this, ConfigStore(this), http) }

    override fun newImageLoader(context: PlatformContext): ImageLoader =
        ImageLoader.Builder(context)
            .components { add(OkHttpNetworkFetcherFactory(callFactory = { http })) }
            .build()
}
